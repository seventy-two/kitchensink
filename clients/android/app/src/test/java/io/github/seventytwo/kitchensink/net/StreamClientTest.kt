package io.github.seventytwo.kitchensink.net

import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import mockwebserver3.Dispatcher
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import mockwebserver3.RecordedRequest
import okhttp3.OkHttpClient
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class StreamClientTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val got = LinkedBlockingQueue<Inbound>()
    private val requests = LinkedBlockingQueue<String>()

    @Before
    fun start() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val target = request.target
                requests += target + " " + request.headers["Authorization"]
                return when {
                    target.startsWith("/ks/api/events") ->
                        MockResponse.Builder().body("""{"events":[{"id":4,"title":"old"},{"id":3,"title":"older"}]}""").build()
                    target.startsWith("/ks/api/stream") -> MockResponse.Builder()
                        .setHeader("Content-Type", "text/event-stream")
                        .body(
                            "event: event\nid: 5\ndata: {\"id\":5,\"title\":\"missed\"}\n\n" +
                                "event: snapshot\ndata: {\"monitor\":\"todo\",\"title\":\"To do\"}\n\n" +
                                "event: snapshot\ndata: not json\n\n" +
                                "event: ready\ndata: {\"last\":5}\n\n" +
                                ": ping\n\n" +
                                "event: event\ndata: {\"id\":6,\"title\":\"new\"}\n\n",
                        ).build()
                    else -> MockResponse.Builder().code(404).build()
                }
            }
        }
        server.start()
    }

    @After
    fun stop() {
        scope.cancel()
        server.close()
    }

    private fun next(): Inbound = got.poll(5, TimeUnit.SECONDS) ?: error("nothing received")

    @Test
    fun loadsHistoryThenStreams() {
        val c = StreamClient(OkHttpClient(), scope) { got += it }
        c.start(server.url("/ks/").toString(), "secret")

        // History first, marked as such.
        assertEquals(Inbound.Ev(io.github.seventytwo.kitchensink.model.Event(id = 4, title = "old"), replayed = true, history = true), next())
        assertEquals(3L, (next() as Inbound.Ev).event.id)
        assertEquals(Inbound.SyncStart, next())
        // Then what was missed, replayed but not history; the unreadable
        // snapshot is skipped.
        val missed = next() as Inbound.Ev
        assertEquals(5L, missed.event.id)
        assertTrue(missed.replayed && !missed.history)
        assertEquals("todo", (next() as Inbound.Snap).snapshot.monitor)
        assertEquals(Inbound.Ready, next())
        val live = next() as Inbound.Ev
        assertEquals(6L, live.event.id)
        assertTrue(!live.replayed)

        assertEquals("/ks/api/events?limit=100 Bearer secret", requests.poll(5, TimeUnit.SECONDS))
        assertEquals("/ks/api/stream?heartbeat=60&since=4 Bearer secret", requests.poll(5, TimeUnit.SECONDS))
        // The stream ended, so it reconnects from the last event it saw,
        // without loading history again.
        assertEquals(Inbound.SyncStart, next())
        assertEquals("/ks/api/stream?heartbeat=60&since=6 Bearer secret", requests.poll(5, TimeUnit.SECONDS))
        c.stop()
    }

    @Test
    fun reportsARejectedToken() {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest) = MockResponse.Builder().code(401).build()
        }
        val c = StreamClient(OkHttpClient(), scope) { got += it }
        c.start(server.url("/").toString(), "wrong")
        val deadline = System.currentTimeMillis() + 5000
        while (c.state.value != ConnState.Offline && System.currentTimeMillis() < deadline) Thread.sleep(20)
        assertEquals(ConnState.Offline, c.state.value)
        assertEquals("the server rejected the token", c.error.value)
        c.stop()
    }

    @Test
    fun waitsToBeSetUp() {
        val c = StreamClient(OkHttpClient(), scope) { got += it }
        c.start("", "")
        assertEquals(ConnState.NotConfigured, c.state.value)
    }
}
