// The dashboard's data: snapshots and events as the server sends them.
// Nothing here depends on Android, so it's unit tested on the JVM. It
// follows the Windows client's model.cpp.
package io.github.seventytwo.kitchensink.model

import java.time.OffsetDateTime
import java.time.format.DateTimeParseException
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.floatOrNull
import kotlinx.serialization.json.longOrNull

enum class Tone { None, Good, Bad, Warn, Muted, Accent }
enum class Align { Left, Right, Center }
enum class Priority { Low, Normal, High }

data class Stat(
    val label: String = "",
    val value: String = "",
    val detail: String = "",
    val tone: Tone = Tone.None,
    val series: List<Float> = emptyList(),
    /** Fixes the sparkline's scale, e.g. 100 for a percentage; 0 fits it to the series. */
    val top: Float = 0f,
    /** A personal figure, e.g. money, hidden on request. */
    val isPrivate: Boolean = false,
    /** A picture by the value, e.g. "rain". */
    val icon: String = "",
    /** 0 to 1, a faint chart behind a chip, e.g. the chance of rain; null draws nothing. */
    val level: Float? = null,
)

data class Column(val title: String = "", val align: Align = Align.Left, val fill: Boolean = false)

data class Cell(val text: String = "", val url: String = "", val tone: Tone = Tone.None, val detail: String = "")

data class Item(
    val title: String = "",
    val subtitle: String = "",
    val detail: String = "",
    val url: String = "",
    val image: String = "",
    val tone: Tone = Tone.None,
    // For checklists.
    val id: String = "",
    val done: Boolean = false,
)

/** One part of a monitor's panel. Only the fields for its type are set. */
data class Block(
    val type: String = "",
    val title: String = "",
    val text: String = "",
    /** A checklist takes new items when set. */
    val addHint: String = "",
    val isPrivate: Boolean = false,
    val stats: List<Stat> = emptyList(),
    val columns: List<Column> = emptyList(),
    val rows: List<List<Cell>> = emptyList(),
    val items: List<Item> = emptyList(),
)

data class Snapshot(
    val monitor: String = "",
    val kind: String = "",
    val title: String = "",
    val error: String = "",
    /** What it's watching, when that isn't obvious. */
    val subtitle: String = "",
    /** When it was updated matters at the moment. */
    val timely: Boolean = false,
    /** Unix seconds; 0 if never polled. */
    val updated: Long = 0,
    val blocks: List<Block> = emptyList(),
    /** The few things most worth seeing at a glance, shown when space is short. */
    val summary: List<Stat> = emptyList(),
    /** "" for tiles, or "chips". */
    val summaryStyle: String = "",
)

data class Event(
    val id: Long = 0,
    val monitor: String = "",
    val key: String = "",
    val title: String = "",
    val body: String = "",
    val url: String = "",
    val image: String = "",
    /** Unix seconds. */
    val time: Long = 0,
    val priority: Priority = Priority.Normal,
    val quiet: Boolean = false,
)

internal val json = Json { ignoreUnknownKeys = true }

// Readers that tolerate a field being missing or another type, as the
// server's JSON is read by hand.
private fun JsonElement?.obj(): JsonObject = this as? JsonObject ?: JsonObject(emptyMap())
private fun JsonObject.str(key: String): String = (this[key] as? JsonPrimitive)?.takeIf { it.isString }?.content ?: ""
private fun JsonObject.flag(key: String): Boolean = (this[key] as? JsonPrimitive)?.booleanOrNull ?: false
private fun JsonObject.array(key: String): List<JsonElement> = this[key] as? JsonArray ?: emptyList()
private fun JsonObject.tone(): Tone = when (str("tone")) {
    "good" -> Tone.Good
    "bad" -> Tone.Bad
    "warn" -> Tone.Warn
    "muted" -> Tone.Muted
    "accent" -> Tone.Accent
    else -> Tone.None
}

private fun parseStat(e: JsonElement): Stat {
    val j = e.obj()
    return Stat(
        label = j.str("label"),
        value = j.str("value"),
        detail = j.str("detail"),
        tone = j.tone(),
        series = j.array("series").mapNotNull { (it as? JsonPrimitive)?.takeIf { p -> !p.isString }?.floatOrNull },
        top = (j["top"] as? JsonPrimitive)?.floatOrNull ?: 0f,
        isPrivate = j.flag("private"),
        icon = j.str("icon"),
        level = (j["level"] as? JsonPrimitive)?.takeIf { !it.isString }?.floatOrNull?.coerceIn(0f, 1f),
    )
}

private fun parseBlock(e: JsonElement): Block {
    val j = e.obj()
    return Block(
        type = j.str("type"),
        title = j.str("title"),
        text = j.str("text"),
        addHint = j.str("addHint"),
        isPrivate = j.flag("private"),
        stats = j.array("stats").map(::parseStat),
        columns = j.array("columns").map {
            val c = it.obj()
            val align = when (c.str("align")) {
                "right" -> Align.Right
                "center" -> Align.Center
                else -> Align.Left
            }
            Column(c.str("title"), align, c.flag("fill"))
        },
        rows = j.array("rows").map { r ->
            (r as? JsonArray ?: emptyList()).map {
                val c = it.obj()
                Cell(c.str("text"), c.str("url"), c.tone(), c.str("detail"))
            }
        },
        items = j.array("items").map {
            val i = it.obj()
            Item(i.str("title"), i.str("subtitle"), i.str("detail"), i.str("url"), i.str("image"), i.tone(), i.str("id"), i.flag("done"))
        },
    )
}

fun parsePriority(s: String): Priority = when (s) {
    "low" -> Priority.Low
    "high" -> Priority.High
    else -> Priority.Normal
}

fun parseSnapshot(e: JsonElement): Snapshot {
    val j = e.obj()
    return Snapshot(
        monitor = j.str("monitor"),
        kind = j.str("kind"),
        title = j.str("title"),
        error = j.str("error"),
        subtitle = j.str("subtitle"),
        timely = j.flag("timely"),
        // Go sends its zero time, year 1, for a monitor that hasn't polled yet.
        updated = maxOf(0, parseTime(j.str("updated"))),
        blocks = j.array("blocks").map(::parseBlock),
        summary = j.array("summary").map(::parseStat),
        summaryStyle = j.str("summaryStyle"),
    )
}

fun parseSnapshot(text: String): Snapshot = parseSnapshot(json.parseToJsonElement(text))

fun parseEvent(e: JsonElement): Event {
    val j = e.obj()
    return Event(
        id = (j["id"] as? JsonPrimitive)?.takeIf { !it.isString }?.longOrNull ?: 0,
        monitor = j.str("monitor"),
        key = j.str("key"),
        title = j.str("title"),
        body = j.str("body"),
        url = j.str("url"),
        image = j.str("image"),
        time = parseTime(j.str("time")),
        priority = parsePriority(j.str("priority")),
        quiet = j.flag("quiet"),
    )
}

fun parseEvent(text: String): Event = parseEvent(json.parseToJsonElement(text))

/** Parses an RFC 3339 time such as "2026-10-10T19:02:00.69+01:00" into Unix seconds, or 0 if it can't. */
fun parseTime(s: String): Long = try {
    OffsetDateTime.parse(s).toEpochSecond()
} catch (_: DateTimeParseException) {
    0
}

/** Describes how long before now a time was: "just now", "4m ago", "2h ago", "3d ago". */
fun ago(then: Long, now: Long): String {
    if (then == 0L) return "never"
    val s = now - then
    return when {
        s < 60 -> "just now"
        s < 3600 -> "${s / 60}m ago"
        s < 86400 -> "${s / 3600}h ago"
        else -> "${s / 86400}d ago"
    }
}

/** Whether a snapshot has any private blocks or stats. */
fun Snapshot.hasPrivate(): Boolean =
    summary.any { it.isPrivate } || blocks.any { b -> b.isPrivate || b.stats.any { it.isPrivate } }

/** The snapshot with its private blocks and stats left out, for when the user has hidden them. */
fun Snapshot.withoutPrivate(): Snapshot = copy(
    summary = summary.filterNot { it.isPrivate },
    blocks = blocks
        .map { it.copy(stats = it.stats.filterNot { s -> s.isPrivate }) }
        // A stats block left with nothing goes too.
        .filterNot { it.isPrivate || (it.type == "stats" && it.stats.isEmpty()) },
)
