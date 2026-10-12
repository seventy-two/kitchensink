package io.github.seventytwo.kitchensink.net

import io.github.seventytwo.kitchensink.model.Event
import io.github.seventytwo.kitchensink.model.Snapshot
import io.github.seventytwo.kitchensink.model.json
import io.github.seventytwo.kitchensink.model.parseEvent
import io.github.seventytwo.kitchensink.model.parseSnapshot
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.runInterruptible
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.jsonObject
import okhttp3.Call
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Something the stream client received. */
sealed interface Inbound {
    data object SyncStart : Inbound
    data class Snap(val snapshot: Snapshot) : Inbound
    /**
     * [replayed] is true for events sent before the stream was ready: the
     * history on first connect, or what was missed while disconnected.
     * [history] is true for events from before the client started.
     */
    data class Ev(val event: Event, val replayed: Boolean, val history: Boolean) : Inbound
    data object Ready : Inbound
}

enum class ConnState { NotConfigured, Connecting, Connected, Offline }

/**
 * Keeps the server's event stream open, reconnecting with backoff and
 * catching up on missed events. [deliver] is called on the client's own
 * thread; the caller hands it to the main thread.
 */
class StreamClient(
    private val http: OkHttpClient,
    private val scope: CoroutineScope,
    private val deliver: (Inbound) -> Unit,
) {
    private val _state = MutableStateFlow(ConnState.NotConfigured)
    val state: StateFlow<ConnState> = _state
    private val _error = MutableStateFlow("")
    val error: StateFlow<String> = _error

    private var job: Job? = null
    @Volatile private var call: Call? = null
    private var base = ""
    private var token = ""

    /** Where the last stream got to, so a restart carries on from there; -1 until history has loaded. */
    private var since = -1L

    /** Connects to the server, replacing any earlier connection. A new server or token starts over. */
    fun start(baseUrl: String, token: String) {
        stop()
        val b = baseUrl.trim().trimEnd('/')
        if (b != base || token != this.token) since = -1
        base = b
        this.token = token
        if (base.isEmpty() || token.isEmpty()) {
            _state.value = ConnState.NotConfigured
            return
        }
        job = scope.launch(Dispatchers.IO) { run(base, token) }
    }

    fun stop() {
        job?.cancel()
        call?.cancel()
        job = null
        if (_state.value != ConnState.NotConfigured) _state.value = ConnState.Offline
    }

    val running: Boolean get() = job?.isActive == true

    /** Asks the server to poll a monitor now. */
    fun refresh(monitor: String) = post("/api/monitors/$monitor/refresh", "")

    /** Sends a monitor an action, such as ticking a to-do; [action] is the JSON the API takes. */
    fun act(monitor: String, action: String) = post("/api/monitors/$monitor/actions", action)

    private fun post(path: String, body: String) {
        val url = base + path
        val t = token
        if (base.isEmpty()) return
        scope.launch(Dispatchers.IO) {
            val req = Request.Builder().url(url).header("Authorization", "Bearer $t")
                .post(body.toRequestBody("application/json".toMediaType())).build()
            try {
                http.newCall(req).execute().close()
            } catch (_: IOException) {
            } catch (_: IllegalArgumentException) {
            }
        }
    }

    private suspend fun run(base: String, token: String) {
        var backoff = 1
        // The server sends a heartbeat every 60 seconds, so two and a half
        // minutes of silence means the connection has gone.
        val stream = http.newBuilder().readTimeout(HEARTBEAT * 5L / 2, TimeUnit.SECONDS).build()
        while (currentCoroutineContext().isActive) {
            _state.value = ConnState.Connecting
            var status = 0
            if (since < 0) {
                val (code, err) = loadHistory(base, token)
                status = code
                if (since < 0) {
                    currentCoroutineContext().ensureActive()
                    fail(if (code == 401) "the server rejected the token" else err)
                    delay((if (code == 401) 60 else backoff) * 1000L)
                    backoff = minOf(backoff * 2, 60)
                    continue
                }
            }

            deliver(Inbound.SyncStart)
            val parser = SseParser()
            var ready = false
            val err = try {
                val req = Request.Builder().url("$base/api/stream?heartbeat=$HEARTBEAT&since=$since")
                    .header("Authorization", "Bearer $token").header("Accept", "text/event-stream").build()
                val c = stream.newCall(req)
                call = c
                runInterruptible {
                    c.execute().use { resp ->
                        status = resp.code
                        if (!resp.isSuccessful) return@runInterruptible describe(resp.code)
                        val source = resp.body.source()
                        val reader = source.inputStream().reader(Charsets.UTF_8)
                        val chars = CharArray(8192)
                        while (true) {
                            val n = reader.read(chars)
                            if (n < 0) break
                            for (m in parser.feed(String(chars, 0, n))) {
                                when (m.event) {
                                    "snapshot" -> parsed { deliver(Inbound.Snap(parseSnapshot(m.data))) }
                                    "event" -> parsed {
                                        val e = parseEvent(m.data)
                                        since = maxOf(since, e.id)
                                        deliver(Inbound.Ev(e, replayed = !ready, history = false))
                                    }
                                    "ready" -> {
                                        ready = true
                                        backoff = 1
                                        _state.value = ConnState.Connected
                                        _error.value = ""
                                        deliver(Inbound.Ready)
                                    }
                                }
                            }
                        }
                        "the server closed the connection"
                    }
                }
            } catch (e: IOException) {
                e.message ?: "connection failed"
            } catch (e: IllegalArgumentException) {
                "the address isn't valid"
            } finally {
                call = null
            }
            currentCoroutineContext().ensureActive()
            fail(err)
            delay((if (status == 401) 60 else backoff) * 1000L)
            backoff = minOf(backoff * 2, 60)
        }
    }

    /** Loads the last 100 events as history, setting [since]. It returns the status and any error. */
    private suspend fun loadHistory(base: String, token: String): Pair<Int, String> = try {
        val req = Request.Builder().url("$base/api/events?limit=100").header("Authorization", "Bearer $token").build()
        val c = http.newCall(req)
        call = c
        runInterruptible {
            c.execute().use { resp ->
                if (!resp.isSuccessful) return@runInterruptible resp.code to describe(resp.code)
                val body = resp.body.string()
                try {
                    val events = json.parseToJsonElement(body).jsonObject["events"] as? JsonArray
                        ?: return@runInterruptible resp.code to "the server sent something unexpected"
                    var last = 0L
                    for (e in events) {
                        val ev = parseEvent(e)
                        last = maxOf(last, ev.id)
                        deliver(Inbound.Ev(ev, replayed = true, history = true))
                    }
                    since = last
                    resp.code to ""
                } catch (_: SerializationException) {
                    resp.code to "the server sent something unexpected"
                } catch (_: IllegalArgumentException) {
                    resp.code to "the server sent something unexpected"
                }
            }
        }
    } catch (e: IOException) {
        0 to (e.message ?: "connection failed")
    } catch (_: IllegalArgumentException) {
        0 to "the address isn't valid"
    } finally {
        call = null
    }

    private fun fail(err: String) {
        _state.value = ConnState.Offline
        _error.value = err
    }

    companion object {
        const val HEARTBEAT = 60

        private fun describe(code: Int) = when (code) {
            401 -> "the server rejected the token"
            404 -> "no kitchensink at that address"
            else -> "the server said $code"
        }

        /** Skips a message that can't be read rather than dropping the connection. */
        private inline fun parsed(f: () -> Unit) {
            try {
                f()
            } catch (_: SerializationException) {
            } catch (_: IllegalArgumentException) {
            }
        }
    }
}
