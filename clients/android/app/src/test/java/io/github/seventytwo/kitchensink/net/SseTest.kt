package io.github.seventytwo.kitchensink.net

import org.junit.Assert.assertEquals
import org.junit.Test

class SseTest {
    @Test
    fun parsesMessagesSplitAnywhere() {
        val p = SseParser()
        val out = mutableListOf<SseMessage>()
        // Split mid-line and mid-CRLF, with a heartbeat comment and a
        // multi-line data field.
        out += p.feed("id: 7\r\nevent: event\r")
        out += p.feed("\ndata: {\"a\":1}\r\n\r\n: ping\n\n")
        out += p.feed("event: snapshot\ndata: x\ndata:y\n")
        assertEquals(1, out.size)
        out += p.feed("\n")
        out += p.feed("data: no event name\n\n")
        out += p.feed("event: empty\n\n") // no data: nothing dispatched
        assertEquals(3, out.size)
        assertEquals(SseMessage("7", "event", "{\"a\":1}"), out[0])
        assertEquals(SseMessage("7", "snapshot", "x\ny"), out[1]) // the ID carries over
        assertEquals("message", out[2].event)
    }
}
