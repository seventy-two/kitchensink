package io.github.seventytwo.kitchensink.model

/** The natural sizes of one chip's parts, in pixels. Heights are 0 for a part that's missing. */
data class ChipMeasure(
    val labelW: Float,
    val labelH: Float,
    val valueW: Float,
    val valueH: Float,
    val detailW: Float = 0f,
    val detailH: Float = 0f,
    val icon: Boolean = false,
)

data class ChipLayout(
    /** Each chip's width. */
    val widths: List<Float>,
    /** Each chip's top-left, from the layout's. */
    val x: List<Float>,
    val y: List<Float>,
    /** Whether each chip starts a row. */
    val rowStart: List<Boolean>,
    /** Every chip's height. */
    val height: Float,
    val labelH: Float,
    val valueH: Float,
    val iconSize: Float,
    /** The whole layout's height. */
    val total: Float,
)

/**
 * Lays out stats as small blocks, all one size so they line up: the label
 * above the value and any detail below. If they nearly fit on one line,
 * they narrow to do so, shortening details that are too wide, though a chip
 * whose label or value wouldn't fit keeps its own width; if labels or
 * values would still be cut short, they flow onto more lines. A port of the
 * Windows client's App::chips.
 */
fun layoutChips(ms: List<ChipMeasure>, width: Float, padX: Float, padY: Float, gap: Float, minShare: Float): ChipLayout {
    val n = ms.size
    if (n == 0) return ChipLayout(emptyList(), emptyList(), emptyList(), emptyList(), 0f, 0f, 0f, 0f, 0f)
    // One size for all, from the widest and tallest of each part. need is
    // what each chip's label and value take: a chip that can't fit them in
    // the shared width gets its own.
    var w = 0f
    var labelH = 0f
    var valueH = 0f
    var detailH = 0f
    var beside = 0f // the widest label or value with an icon by it
    var icons = false
    val need = FloatArray(n)
    for ((i, m) in ms.withIndex()) {
        w = maxOf(w, m.labelW, m.detailW, m.valueW)
        need[i] = maxOf(m.labelW, m.valueW)
        labelH = maxOf(labelH, m.labelH)
        detailH = maxOf(detailH, m.detailH)
        valueH = maxOf(valueH, m.valueH)
        // An icon, if any, goes right of the label and value, as tall as both
        // if there's room, as tall as the value at least.
        if (m.icon) {
            icons = true
            beside = maxOf(beside, m.valueW, m.labelW)
            val withIcon = m.valueW + gap + m.valueH * 1.2f
            w = maxOf(w, withIcon)
            need[i] = maxOf(need[i], withIcon)
        }
    }
    if (icons) w = maxOf(w, beside + gap + labelH + valueH)
    w += padX * 2
    for (i in 0 until n) need[i] = minOf(need[i] + padX * 2, width)
    val oneRow = (width - gap * (n - 1)) / n
    val ws = MutableList(n) { minOf(w, width) }
    if (w > oneRow && oneRow >= minShare) {
        // All on one row: the chips that need more than an equal share take
        // it, and the rest share what's left.
        var share = oneRow
        for (pass in 0 until n) {
            var taken = 0f
            var wide = 0
            for (x in need) if (x > share) { taken += x; wide++ }
            if (wide == n) break
            val next = (width - gap * (n - 1) - taken) / (n - wide)
            if (next == share) break
            share = next
        }
        // If that still leaves a label or value cut short, they flow onto
        // more lines at their full width instead.
        var total = gap * (n - 1)
        for (x in need) total += maxOf(share, x)
        if (share >= minShare && total <= width + 0.5f) {
            w = share
            for (i in 0 until n) ws[i] = maxOf(share, need[i])
        }
    }
    val h = padY * 2 + labelH + valueH + detailH
    val iconSize = (w - padX * 2 - beside - gap).coerceIn(valueH * 1.2f, maxOf(valueH * 1.2f, labelH + valueH))

    val xs = MutableList(n) { 0f }
    val ys = MutableList(n) { 0f }
    val starts = MutableList(n) { false }
    var x = 0f
    var y = 0f
    for (i in 0 until n) {
        if (x > 0 && x + ws[i] > width + 0.5f) {
            x = 0f
            y += h + gap
        }
        starts[i] = x == 0f
        xs[i] = x
        ys[i] = y
        x += ws[i] + gap
    }
    return ChipLayout(ws, xs, ys, starts, h, labelH, valueH, Math.round(iconSize).toFloat(), y + h)
}
