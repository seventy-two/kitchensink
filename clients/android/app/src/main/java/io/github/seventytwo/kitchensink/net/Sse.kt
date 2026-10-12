package io.github.seventytwo.kitchensink.net

/** One server-sent event. [event] is "message" if the stream didn't name it; [id] is the last ID the stream set. */
data class SseMessage(val id: String, val event: String, val data: String)

/** Parses server-sent events from the response body, fed in whatever chunks the network delivers. */
class SseParser {
    private val buf = StringBuilder()
    private var lastId = ""
    private var event = ""
    private val data = StringBuilder()
    private var hasData = false
    private var skipLf = false

    /** Adds text from the stream and returns the messages it completes. Comment lines (heartbeats) produce nothing. */
    fun feed(chunk: CharSequence): List<SseMessage> {
        val out = mutableListOf<SseMessage>()
        // Lines end in \n, \r\n or \r. A \r\n can be split across chunks, so
        // after a \r the next \n is skipped even if it's in the next chunk.
        for (c in chunk) {
            if (skipLf) {
                skipLf = false
                if (c == '\n') continue
            }
            if (c == '\r' || c == '\n') {
                line(buf.toString(), out)
                buf.setLength(0)
                skipLf = c == '\r'
                continue
            }
            buf.append(c)
        }
        return out
    }

    private fun line(l: String, out: MutableList<SseMessage>) {
        if (l.isEmpty()) {
            if (hasData) out += SseMessage(lastId, event.ifEmpty { "message" }, data.toString())
            event = ""
            data.setLength(0)
            hasData = false
            return
        }
        if (l[0] == ':') return
        val colon = l.indexOf(':')
        val field = if (colon >= 0) l.substring(0, colon) else l
        val value = if (colon >= 0) l.substring(colon + 1).removePrefix(" ") else ""
        when (field) {
            "data" -> {
                if (hasData) data.append('\n')
                data.append(value)
                hasData = true
            }
            "event" -> event = value
            "id" -> lastId = value
            // "retry" and unknown fields are ignored; the client manages reconnects.
        }
    }
}
