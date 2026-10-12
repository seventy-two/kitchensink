package io.github.seventytwo.kitchensink.model

/** What's been cleared from Recent: every event up to [through], and [ids]. */
data class Cleared(val through: Long = 0, val ids: Set<Long> = emptySet())

/**
 * The dashboard's state, built from the stream. It isn't thread safe: the
 * app only touches it from the main thread.
 */
class Dashboard {
    private val snapshots = mutableListOf<Snapshot>()
    private val events = ArrayDeque<Event>()
    private var syncing = false
    private val synced = mutableSetOf<String>()

    var lastEventId = 0L
        private set
    var cleared = Cleared()
        private set

    /** The monitors, in the server's config order. */
    val snapshotList: List<Snapshot> get() = snapshots.toList()

    /** Events, newest first. */
    val eventList: List<Event> get() = events.toList()

    /**
     * Called when the stream connects. Monitors the server doesn't send a
     * snapshot for before [endSync] are dropped, so ones removed from the
     * config disappear.
     */
    fun beginSync() {
        syncing = true
        synced.clear()
    }

    fun endSync() {
        if (!syncing) return
        syncing = false
        snapshots.removeAll { it.monitor !in synced }
    }

    fun applySnapshot(s: Snapshot) {
        if (syncing) {
            // The server sends snapshots in config order; keep that order.
            val pos = synced.size
            synced += s.monitor
            snapshots.removeAll { it.monitor == s.monitor }
            snapshots.add(minOf(pos, snapshots.size), s)
            return
        }
        val i = snapshots.indexOfFirst { it.monitor == s.monitor }
        if (i >= 0) snapshots[i] = s else snapshots += s
    }

    /** Stores an event and reports whether it was new. Cleared ones aren't stored again, e.g. when history is replayed. */
    fun addEvent(e: Event): Boolean {
        if (events.any { it.id == e.id }) return false
        lastEventId = maxOf(lastEventId, e.id)
        if (e.id <= cleared.through || e.id in cleared.ids) return false
        // Keep newest first; events nearly always arrive in order.
        var i = 0
        while (i < events.size && events[i].id > e.id) i++
        events.add(i, e)
        while (events.size > MAX_EVENTS) events.removeLast()
        return true
    }

    /** Removes an event from Recent. It stays cleared through reconnects and, via [cleared], restarts. */
    fun clear(id: Long) {
        events.removeAll { it.id == id }
        if (id <= cleared.through) return
        cleared = cleared.copy(ids = (cleared.ids + id).sorted().takeLast(MAX_CLEARED).toSet())
    }

    /** Removes every event so far. */
    fun clearAll() {
        events.clear()
        val through = maxOf(cleared.through, lastEventId)
        cleared = Cleared(through, cleared.ids.filter { it > through }.toSet())
    }

    fun restoreCleared(c: Cleared) {
        cleared = Cleared(c.through, c.ids.filter { it > c.through }.sorted().takeLast(MAX_CLEARED).toSet())
        events.removeAll { it.id <= cleared.through || it.id in cleared.ids }
    }

    /** A monitor's title, or its ID if it has no snapshot. */
    fun titleOf(monitor: String): String = snapshots.firstOrNull { it.monitor == monitor && it.title.isNotEmpty() }?.title ?: monitor

    companion object {
        const val MAX_EVENTS = 300
        /** At most this many single cleared events are remembered, the newest. */
        const val MAX_CLEARED = 500
    }
}
