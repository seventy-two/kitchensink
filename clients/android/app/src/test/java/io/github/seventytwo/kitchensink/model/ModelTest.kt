package io.github.seventytwo.kitchensink.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ModelTest {
    @Test
    fun parsesTimes() {
        assertEquals(0L, parseTime("1970-01-01T00:00:00Z"))
        assertEquals(1791655651L, parseTime("2026-10-10T18:07:31.499738752Z"))
        assertEquals(1791655651L, parseTime("2026-10-10T19:07:31+01:00"))
        assertEquals(1791655651L, parseTime("2026-10-10T13:07:31.5-05:00"))
        assertEquals(951782400L, parseTime("2000-02-29T00:00:00Z"))
        assertEquals(-62135596800L, parseTime("0001-01-01T00:00:00Z")) // Go's zero time
        assertEquals(0L, parseTime(""))
        assertEquals(0L, parseTime("2026-10-10 18:07:31Z"))
        assertEquals(0L, parseTime("2026-10-10T18:07:31")) // no zone
    }

    @Test
    fun saysHowLongAgo() {
        assertEquals("just now", ago(1000, 1030))
        assertEquals("4m ago", ago(1000, 1000 + 4 * 60 + 59))
        assertEquals("2h ago", ago(1000, 1000 + 2 * 3600))
        assertEquals("3d ago", ago(1000, 1000 + 3 * 86400 + 5))
        assertEquals("never", ago(0, 1000))
    }

    @Test
    fun parsesSnapshots() {
        val s = parseSnapshot(
            """{
            "monitor": "weather", "kind": "weather", "title": "Weather",
            "updated": "2026-10-10T18:07:31Z", "error": "site down", "subtitle": "Mazda3 · Automatic", "timely": true,
            "blocks": [
                {"type": "stats", "title": "Bristol", "stats": [
                    {"label": "Now", "value": "13°C", "detail": "Clear", "tone": "accent", "series": [0, 0.4, 1.5], "icon": "rain", "level": 1.4}
                ]},
                {"type": "table", "columns": [{"title": "Day"}, {"title": "High", "align": "right", "fill": true}],
                 "rows": [[{"text": "Today", "url": "https://example.com", "detail": "dry"}, {"text": "15°", "tone": "bad"}]]},
                {"type": "list", "items": [{"title": "Car", "image": "https://example.com/a.jpg", "tone": "warn"}]},
                {"type": "text", "text": "hello", "unknown": {"nested": true}},
                {"type": "checklist", "addHint": "Add a task…", "items": [{"id": "a1", "title": "Bins", "done": true}, {"id": "b2", "title": "Post"}]}
            ],
            "summary": [{"label": "Now", "value": "13°C", "tone": "good", "series": [1, 2]}, {"label": "Rain"}],
            "summaryStyle": "chips"
        }""",
        )
        assertEquals("weather", s.monitor)
        assertEquals("site down", s.error)
        assertEquals("Mazda3 · Automatic", s.subtitle)
        assertTrue(s.timely)
        assertEquals(1791655651L, s.updated)
        assertEquals(5, s.blocks.size)
        assertEquals("Add a task…", s.blocks[4].addHint)
        assertEquals("a1", s.blocks[4].items[0].id)
        assertTrue(s.blocks[4].items[0].done)
        assertFalse(s.blocks[4].items[1].done)
        val now = s.blocks[0].stats[0]
        assertEquals("13°C", now.value)
        assertEquals(Tone.Accent, now.tone)
        assertEquals(listOf(0f, 0.4f, 1.5f), now.series)
        assertEquals("rain", now.icon)
        assertEquals(1f, now.level) // clamped
        assertEquals(null, s.summary[1].level)
        assertEquals(Align.Right, s.blocks[1].columns[1].align)
        assertFalse(s.blocks[1].columns[0].fill)
        assertTrue(s.blocks[1].columns[1].fill)
        assertEquals("chips", s.summaryStyle)
        assertEquals(Tone.Good, s.summary[0].tone)
        assertEquals("https://example.com", s.blocks[1].rows[0][0].url)
        assertEquals("dry", s.blocks[1].rows[0][0].detail)
        assertEquals(Tone.Bad, s.blocks[1].rows[0][1].tone)
        assertEquals(Tone.Warn, s.blocks[2].items[0].tone)
        assertEquals("hello", s.blocks[3].text)

        // Wrong types and missing fields don't throw.
        val odd = parseSnapshot("""{"monitor": 5, "blocks": {"x": 1}, "timely": "yes", "summary": [3, "x"]}""")
        assertFalse(odd.timely)
        assertEquals("", odd.monitor)
        assertTrue(odd.blocks.isEmpty())
        assertEquals(2, odd.summary.size)
        assertEquals(0L, parseSnapshot("""{"updated": "0001-01-01T00:00:00Z"}""").updated)
    }

    @Test
    fun parsesEvents() {
        val e = parseEvent(
            """{"id": 42, "monitor": "nfl", "key": "final:1", "time": "2026-10-10T18:07:31Z",
                "title": "Final: TB 24 – 16 DAL", "priority": "high", "quiet": true}""",
        )
        assertEquals(42L, e.id)
        assertEquals("Final: TB 24 – 16 DAL", e.title)
        assertEquals(1791655651L, e.time)
        assertEquals(Priority.High, e.priority)
        assertTrue(e.quiet)
        assertEquals(Priority.Normal, parseEvent("{}").priority)
    }

    @Test
    fun hidesPrivateFigures() {
        val s = parseSnapshot(
            """{
            "monitor": "stocks",
            "summary": [{"label": "Today", "value": "+£10", "private": true}, {"label": "AMD", "value": "+1%"}],
            "blocks": [
                {"type": "stats", "private": true, "stats": [{"label": "Portfolio", "value": "£1,000"}]},
                {"type": "stats", "stats": [{"label": "Mine", "value": "£5", "private": true}, {"label": "Public", "value": "1"}]},
                {"type": "stats", "stats": [{"label": "Only mine", "value": "£5", "private": true}]},
                {"type": "table", "private": true, "columns": [{"title": "Day"}]},
                {"type": "table", "columns": [{"title": "Symbol"}]}
            ]}""",
        )
        assertTrue(s.hasPrivate())
        val hidden = s.withoutPrivate()
        assertFalse(hidden.hasPrivate())
        assertEquals(listOf("AMD"), hidden.summary.map { it.label })
        // The private blocks go, as does a stats block left with nothing.
        assertEquals(listOf("stats", "table"), hidden.blocks.map { it.type })
        assertEquals(listOf("Public"), hidden.blocks[0].stats.map { it.label })
        assertEquals("Symbol", hidden.blocks[1].columns[0].title)
    }
}
