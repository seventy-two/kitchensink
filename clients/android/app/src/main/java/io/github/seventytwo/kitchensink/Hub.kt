package io.github.seventytwo.kitchensink

import android.content.Context
import android.os.SystemClock
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import io.github.seventytwo.kitchensink.data.Config
import io.github.seventytwo.kitchensink.data.ConfigStore
import io.github.seventytwo.kitchensink.model.Dashboard
import io.github.seventytwo.kitchensink.model.Event
import io.github.seventytwo.kitchensink.model.Snapshot
import io.github.seventytwo.kitchensink.net.ConnState
import io.github.seventytwo.kitchensink.net.Inbound
import io.github.seventytwo.kitchensink.net.StreamClient
import io.github.seventytwo.kitchensink.notify.Notifier
import io.github.seventytwo.kitchensink.service.LiveService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.OkHttpClient

/**
 * The app's state: settings, the stream and the dashboard built from it.
 * There's one for the process, shared by the activity, the background
 * service and the notifications. Everything here runs on the main thread.
 */
class Hub(private val context: Context, private val store: ConfigStore, http: OkHttpClient) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val inbox = Channel<Inbound>(Channel.UNLIMITED)
    private var dashboard = Dashboard()
    private val notifier = Notifier(context)

    val client = StreamClient(http, scope) { inbox.trySend(it) }
    val connState: StateFlow<ConnState> = client.state
    val connError: StateFlow<String> = client.error

    private val _config = MutableStateFlow(store.load())
    val config: StateFlow<Config> = _config

    private val _snapshots = MutableStateFlow<List<Snapshot>>(emptyList())
    val snapshots: StateFlow<List<Snapshot>> = _snapshots
    private val _events = MutableStateFlow<List<Event>>(emptyList())
    val events: StateFlow<List<Event>> = _events

    /** Monitors being refreshed by hand, with when they were asked (elapsed milliseconds). */
    private val _refreshing = MutableStateFlow<Map<String, Long>>(emptyMap())
    val refreshing: StateFlow<Map<String, Long>> = _refreshing

    /** Checklist ticks shown before the server confirms them, by "monitor/item"; a snapshot replaces them. */
    private val _ticked = MutableStateFlow<Map<String, Boolean>>(emptyMap())
    val ticked: StateFlow<Map<String, Boolean>> = _ticked

    /** Until when something new keeps an idle screen from dimming (Unix seconds). */
    private val _wakeUntil = MutableStateFlow(0L)
    val wakeUntil: StateFlow<Long> = _wakeUntil

    private var foreground = false
    private var stopLater: Job? = null

    init {
        dashboard.restoreCleared(_config.value.cleared)
        scope.launch {
            for (m in inbox) {
                handle(m)
                // Publish once a burst has been taken in.
                if (inbox.isEmpty) publish()
            }
        }
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) {
                foreground = true
                stopLater?.cancel()
                notifier.clearEvents()
                ensureRunning()
            }

            override fun onStop(owner: LifecycleOwner) {
                foreground = false
                // Without the background service, let go of the connection
                // a little after leaving, so a quick switch away and back
                // doesn't reconnect.
                if (!_config.value.background) stopLater = scope.launch {
                    delay(15_000)
                    client.stop()
                }
            }
        })
    }

    /** Starts the stream, and the background service if it's wanted, unless they're running. */
    fun ensureRunning() {
        val c = _config.value
        ensureStream()
        if (c.background && c.configured) LiveService.start(context) else LiveService.stop(context)
    }

    /** Starts the stream unless it's running. */
    fun ensureStream() {
        val c = _config.value
        if (!client.running && c.configured) client.start(c.url, c.token)
    }

    private fun handle(m: Inbound) {
        when (m) {
            Inbound.SyncStart -> dashboard.beginSync()
            Inbound.Ready -> dashboard.endSync()
            is Inbound.Snap -> {
                _refreshing.value -= m.snapshot.monitor
                val prefix = m.snapshot.monitor + "/"
                if (_ticked.value.keys.any { it.startsWith(prefix) }) _ticked.value = _ticked.value.filterKeys { !it.startsWith(prefix) }
                dashboard.applySnapshot(m.snapshot)
            }
            is Inbound.Ev -> {
                val e = m.event
                if (!dashboard.addEvent(e)) return
                // History from before the app started has already been seen
                // (or missed for good); events missed while offline are new.
                if (m.history || e.quiet) return
                _wakeUntil.value = System.currentTimeMillis() / 1000 + 2 * 60
                val c = _config.value
                if (!foreground && c.notify && e.priority >= c.minPriority) notifier.hold(e) { dashboard.titleOf(it) }
            }
        }
    }

    private fun publish() {
        _snapshots.value = dashboard.snapshotList
        _events.value = dashboard.eventList
    }

    fun update(f: (Config) -> Config) {
        val old = _config.value
        var c = f(old)
        if (c == old) return
        val reconnect = c.url != old.url || c.token != old.token
        // Another server's events have other IDs, so what was cleared goes.
        if (c.url != old.url) c = c.withCleared(io.github.seventytwo.kitchensink.model.Cleared())
        _config.value = c
        store.save(c)
        if (reconnect) {
            client.stop()
            dashboard = Dashboard().apply { restoreCleared(c.cleared) }
            publish()
        }
        ensureRunning()
    }

    fun refresh(monitor: String) {
        _refreshing.value += monitor to SystemClock.elapsedRealtime()
        client.refresh(monitor)
        // Clears itself if no snapshot comes, so the button comes back.
        scope.launch {
            delay(15_000)
            _refreshing.value -= monitor
        }
    }

    fun toggle(monitor: String, id: String, done: Boolean) {
        _ticked.value += "$monitor/$id" to done
        client.act(monitor, buildJsonObject { put("type", "toggle"); put("id", id) }.toString())
        scope.launch {
            delay(10_000)
            _ticked.value -= "$monitor/$id"
        }
    }

    fun delete(monitor: String, id: String) =
        client.act(monitor, buildJsonObject { put("type", "delete"); put("id", id) }.toString())

    fun add(monitor: String, text: String) {
        if (text.isBlank()) return
        client.act(monitor, buildJsonObject { put("type", "add"); put("text", text.trim()) }.toString())
    }

    fun clearEvent(id: Long) {
        dashboard.clear(id)
        update { it.withCleared(dashboard.cleared) }
        publish()
    }

    fun clearAll() {
        dashboard.clearAll()
        update { it.withCleared(dashboard.cleared) }
        publish()
    }

    fun titleOf(monitor: String) = dashboard.titleOf(monitor)
}
