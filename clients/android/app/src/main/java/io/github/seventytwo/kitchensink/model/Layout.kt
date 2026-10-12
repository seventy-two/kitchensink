// Layout decisions, kept apart from Compose so they can be tested: panel
// order, which panels open in the compact layout, and OLED care.
package io.github.seventytwo.kitchensink.model

import kotlin.math.cos
import kotlin.math.roundToInt
import kotlin.math.sin

/**
 * Orders panel IDs by a saved order. Panels in [saved] come in that order;
 * ones it doesn't mention (e.g. added since) keep their place after the
 * panel they follow by default. IDs in [saved] that no longer exist are
 * ignored.
 */
fun arrange(defaults: List<String>, saved: List<String>): List<String> {
    val out = saved.filter { it in defaults }.distinct().toMutableList()
    for ((i, id) in defaults.withIndex()) {
        if (id in out) continue
        // After the panel it follows by default, or first if none.
        val pos = if (i > 0) out.indexOf(defaults[i - 1]).let { if (it >= 0) it + 1 else 0 } else 0
        out.add(pos, id)
    }
    return out
}

/** [order] with [id] moved by [by] places (negative is earlier), staying within the list. */
fun shift(order: List<String>, id: String, by: Int): List<String> {
    val from = order.indexOf(id)
    if (from < 0) return order
    val to = (from + by).coerceIn(0, order.size - 1)
    return order.toMutableList().apply { removeAt(from); add(to, id) }
}

/** How much of a monitor the compact layout shows: its summary row, its full panel, or its panel cut short to fit, scrolling inside. */
enum class Show { Row, Full, Clipped }

/** A choice made by hand in the compact layout. */
enum class Pin { Auto, Open, Closed }

/** One monitor's measured heights in the compact layout. */
data class Panel(
    /** Collapsed to its summary. */
    val row: Float,
    /** With its full panel; 0 if not measured. */
    val full: Float,
    /** What clipping adds to the row besides the scrolling area itself. */
    val overhead: Float = 0f,
    val pin: Pin = Pin.Auto,
)

data class Fit(val show: List<Show>, val body: Float = 0f)

/**
 * Fills a column of [height] with panels, [gap] apart. Pinned ones are
 * open or closed as chosen; the rest open in order while they fit whole.
 * If space is left, the first that didn't fit takes it, scrolling, as long
 * as that gives it at least [minBody].
 */
fun fitPanels(panels: List<Panel>, height: Float, gap: Float, minBody: Float): Fit {
    val show = MutableList(panels.size) { Show.Row }
    var total = 0f
    for ((i, p) in panels.withIndex()) {
        val open = p.pin == Pin.Open && p.full > 0
        if (open) show[i] = Show.Full
        total += (if (open) p.full else p.row) + gap
    }
    for ((i, p) in panels.withIndex()) {
        if (p.pin != Pin.Auto || p.full <= 0 || total - p.row + p.full > height) continue
        show[i] = Show.Full
        total += p.full - p.row
    }
    var body = 0f
    for ((i, p) in panels.withIndex()) {
        if (p.pin != Pin.Auto || show[i] != Show.Row || p.full <= 0) continue
        val b = height - total - p.overhead
        if (b >= minBody) {
            show[i] = Show.Clipped
            body = b
        }
        break
    }
    return Fit(show, body)
}

/**
 * The pins after a panel is tapped open or closed: the choice is kept only
 * while it differs from what would happen by itself, so a pin doesn't
 * outlive its use. [open] is whether it's being opened.
 */
fun repin(panels: List<Panel>, index: Int, open: Boolean, height: Float, gap: Float, minBody: Float): Pin {
    val auto = panels.toMutableList().also { it[index] = it[index].copy(pin = Pin.Auto) }
    val automatic = fitPanels(auto, height, gap, minBody).show[index] != Show.Row
    return when {
        open == automatic -> Pin.Auto
        open -> Pin.Open
        else -> Pin.Closed
    }
}

// OLED care: small, regular changes so nothing sits on the same pixels for
// hours on an OLED screen. Times are Unix seconds.

/** How far to move the contents at time [t]: one of eight points on a circle of radius [r], moving on every three minutes. */
fun oledShift(t: Long, r: Float): Pair<Float, Float> {
    val angle = ((t / (3 * 60)) % 8) * Math.PI / 4
    return (cos(angle) * r).roundToInt().toFloat() to (sin(angle) * r).roundToInt().toFloat()
}

/** Whether the Recent column swaps sides at time [t], which it does every other hour. */
fun oledSwapSides(t: Long): Boolean = (t / 3600) % 2 == 1L

/** Whether to dim at [now]: ten minutes after the last activity, unless something new has woken it until [wakeUntil]. */
fun oledDim(now: Long, lastActivity: Long, wakeUntil: Long): Boolean = now - lastActivity >= 10 * 60 && now >= wakeUntil

/**
 * Whether a screen of this size, in dp, shows each monitor's summary rather
 * than its full panels. It takes the current choice so that resizing near
 * the boundary doesn't flicker.
 */
fun useCompact(compact: Boolean, width: Float, height: Float): Boolean {
    val slack = if (compact) 24f else -24f
    return height < 900 + slack || width < 840 + slack
}

/** Whether there's room for the Recent column beside the panels; otherwise it's a page of its own. */
fun feedBeside(width: Float): Boolean = width >= 720
