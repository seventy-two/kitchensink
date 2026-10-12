package io.github.seventytwo.kitchensink.model

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DashboardTest {
    private fun snap(id: String) = Snapshot(monitor = id, title = "T$id")
    private fun ev(id: Long) = Event(id = id)

    @Test
    fun keepsTheServersMonitorsInOrder() {
        val m = Dashboard()
        m.beginSync()
        m.applySnapshot(snap("a"))
        m.applySnapshot(snap("b"))
        m.endSync()
        m.applySnapshot(snap("c")) // a live update for a new monitor
        assertEquals(3, m.snapshotList.size)

        // Reconnect: the server now has b and a, in that order, and no c.
        m.beginSync()
        m.applySnapshot(snap("b"))
        m.applySnapshot(snap("a"))
        m.endSync()
        assertEquals(listOf("b", "a"), m.snapshotList.map { it.monitor })
        assertEquals("Ta", m.titleOf("a"))
        assertEquals("zzz", m.titleOf("zzz"))
    }

    @Test
    fun keepsEventsNewestFirst() {
        val m = Dashboard()
        assertTrue(m.addEvent(ev(2)))
        assertFalse(m.addEvent(ev(2)))
        assertTrue(m.addEvent(ev(5)))
        assertTrue(m.addEvent(ev(3)))
        assertEquals(listOf(5L, 3L, 2L), m.eventList.map { it.id })
        assertEquals(5L, m.lastEventId)
        for (i in 10L until 10L + Dashboard.MAX_EVENTS + 5) m.addEvent(ev(i))
        assertEquals(Dashboard.MAX_EVENTS, m.eventList.size)
    }

    @Test
    fun clearedEventsStayCleared() {
        // One, then all; neither comes back when history is replayed, but
        // newer events still arrive.
        val c = Dashboard()
        for (i in 1L..4L) c.addEvent(ev(i))
        c.clear(3)
        assertEquals(3, c.eventList.size)
        assertFalse(c.addEvent(ev(3)))
        assertTrue(3L in c.cleared.ids)
        c.clearAll()
        assertTrue(c.eventList.isEmpty())
        assertEquals(4L, c.cleared.through)
        assertTrue(c.cleared.ids.isEmpty())
        assertFalse(c.addEvent(ev(2)))
        assertTrue(c.addEvent(ev(5)))
        assertEquals(5L, c.lastEventId)

        // After a restart: what was cleared before is dropped, the rest kept.
        val r = Dashboard()
        for (i in 1L..6L) r.addEvent(ev(i))
        r.restoreCleared(Cleared(2, setOf(1, 5)))
        assertEquals(listOf(6L, 4L, 3L), r.eventList.map { it.id })
        assertEquals(setOf(5L), r.cleared.ids)
        for (i in 100L until 100L + Dashboard.MAX_CLEARED + 10) r.clear(i)
        assertEquals(Dashboard.MAX_CLEARED, r.cleared.ids.size)
        assertFalse(5L in r.cleared.ids)
    }
}
