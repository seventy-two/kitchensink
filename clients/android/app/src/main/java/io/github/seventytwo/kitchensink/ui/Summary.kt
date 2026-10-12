package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.TextMeasurer
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.draw.DrawScopeShapes
import io.github.seventytwo.kitchensink.draw.Vec2
import io.github.seventytwo.kitchensink.draw.drawWeatherIcon
import io.github.seventytwo.kitchensink.draw.isWeatherIcon
import io.github.seventytwo.kitchensink.model.ChipMeasure
import io.github.seventytwo.kitchensink.model.Stat
import io.github.seventytwo.kitchensink.model.Tone
import io.github.seventytwo.kitchensink.model.layoutChips
import kotlin.math.PI
import kotlin.math.cos

/** A monitor's summary: chips if it asks for them, else tiles. */
@Composable
fun Summary(stats: List<Stat>, style: String, modifier: Modifier = Modifier) {
    if (stats.isEmpty()) return
    if (style == "chips") Chips(stats, modifier) else Tiles(stats, modifier)
}

/** Each line of [text] measured on one line, shortened with an ellipsis to fit [width] when given. */
private fun measureLines(m: TextMeasurer, text: String, style: TextStyle, width: Int? = null): List<TextLayoutResult> =
    text.split('\n').map {
        m.measure(
            it, style, overflow = TextOverflow.Ellipsis, softWrap = false, maxLines = 1,
            constraints = if (width != null) Constraints(maxWidth = maxOf(width, 0)) else Constraints(),
        )
    }

/**
 * Stats as small blocks filled with their tone's colour, all one size so
 * they line up; see [layoutChips]. A stat's level is drawn faintly behind,
 * joined to its neighbours' as one chart, e.g. the chance of rain.
 */
@Composable
fun Chips(stats: List<Stat>, modifier: Modifier = Modifier) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val width = constraints.maxWidth.toFloat()
        val px = with(density) { 6.dp.toPx() }
        val py = with(density) { 4.dp.toPx() }
        val gap = with(density) { 4.dp.toPx() }
        val minShare = with(density) { 44.dp.toPx() }
        val caption = mt.caption
        val value = mt.heading
        val layout = remember(stats, width, caption, value) {
            val ms = stats.map { s ->
                val label = measureLines(measurer, s.label, caption)
                val v = measurer.measure(s.value, value, softWrap = false, maxLines = 1)
                val d = if (s.detail.isEmpty()) emptyList() else measureLines(measurer, s.detail, caption)
                ChipMeasure(
                    labelW = label.maxOf { it.size.width }.toFloat(),
                    labelH = label.sumOf { it.size.height }.toFloat(),
                    valueW = v.size.width.toFloat(),
                    valueH = if (s.value.isEmpty()) 0f else v.size.height.toFloat(),
                    detailW = d.maxOfOrNull { it.size.width }?.toFloat() ?: 0f,
                    detailH = d.sumOf { it.size.height }.toFloat(),
                    icon = isWeatherIcon(s.icon),
                )
            }
            layoutChips(ms, width, px, py, gap, minShare)
        }
        val levels = stats.any { it.level != null }
        val icons = p.iconColours()
        val heightDp = with(density) { layout.total.toDp() }
        Canvas(Modifier.fillMaxWidth().height(heightDp)) {
            val shapes = DrawScopeShapes(this)
            for ((i, s) in stats.withIndex()) {
                val ax = layout.x[i]
                val ay = layout.y[i]
                val w = layout.widths[i]
                val h = layout.height
                val inner = (w - px * 2).toInt()
                val tone = p.tone(s.tone)
                val coloured = s.tone == Tone.Good || s.tone == Tone.Bad || s.tone == Tone.Warn || s.tone == Tone.Accent
                drawRect(if (coloured) tone.copy(alpha = 0.2f) else p.frame, Offset(ax, ay), Size(w, h))
                if (levels) {
                    // The levels as one faint chart behind the row: through the
                    // middle of each block at its own level, and at each edge
                    // halfway to its neighbour's.
                    fun lv(j: Int) = stats[j].level ?: 0f
                    val mid = lv(i)
                    val left = if (layout.rowStart[i]) mid else (lv(i - 1) + mid) / 2
                    val right = if (i + 1 == stats.size || layout.rowStart[i + 1]) mid else (lv(i + 1) + mid) / 2
                    val steps = 10
                    val pts = (0..steps * 2).map { k ->
                        val u = k.toFloat() / (steps * 2)
                        val from = if (k <= steps) left else mid
                        val to = if (k <= steps) mid else right
                        val e = (1 - cos((if (k <= steps) k else k - steps).toFloat() / steps * PI.toFloat())) / 2
                        Offset(ax + u * w, ay + h - (from + (to - from) * e) * h)
                    }
                    val fill = Path().apply {
                        moveTo(ax, ay + h)
                        pts.forEach { lineTo(it.x, it.y) }
                        lineTo(ax + w, ay + h)
                        close()
                    }
                    drawPath(fill, p.accent.copy(alpha = 0.13f))
                    // At 0% the line still shows, along the bottom.
                    val thick = 1.25f * density.density
                    val floor = ay + h - thick / 2
                    val line = Path().apply {
                        pts.forEachIndexed { k, o -> if (k == 0) moveTo(o.x, minOf(o.y, floor)) else lineTo(o.x, minOf(o.y, floor)) }
                    }
                    drawPath(line, p.accent.copy(alpha = 0.5f), style = androidx.compose.ui.graphics.drawscope.Stroke(thick))
                }
                var ty = ay + py
                for (l in measureLines(measurer, s.label, caption.copy(color = if (s.tone == Tone.Muted) p.muted else p.text), inner)) {
                    drawText(l, topLeft = Offset(ax + px, ty))
                    ty += l.size.height
                }
                val icon = isWeatherIcon(s.icon)
                val valueColour = if (coloured) tone else if (s.tone == Tone.Muted) p.muted else p.text
                val vw = inner - if (icon) (layout.iconSize + gap).toInt() else 0
                val v = measurer.measure(
                    s.value, value.copy(color = valueColour), overflow = TextOverflow.Ellipsis, softWrap = false, maxLines = 1,
                    constraints = Constraints(maxWidth = maxOf(vw, 0)),
                )
                drawText(v, topLeft = Offset(ax + px, ay + py + layout.labelH))
                if (icon) {
                    val size = layout.iconSize
                    drawWeatherIcon(shapes, s.icon, Vec2(ax + w - px - size, ay + py + layout.labelH + layout.valueH - size), size, icons)
                }
                if (s.detail.isNotEmpty()) {
                    var dy = ay + py + layout.labelH + layout.valueH
                    for (l in measureLines(measurer, s.detail, caption.copy(color = p.muted), inner)) {
                        drawText(l, topLeft = Offset(ax + px, dy))
                        dy += l.size.height
                    }
                }
            }
        }
    }
}

/** Stats as tiles of label, value and detail, flowing onto more lines when they don't fit. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun Tiles(stats: List<Stat>, modifier: Modifier = Modifier) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val details = stats.any { it.detail.isNotEmpty() }
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val max = maxWidth
        FlowRow(horizontalArrangement = Arrangement.spacedBy(24.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            for (s in stats) {
                Column(Modifier.widthIn(max = max)) {
                    Text(s.label, style = mt.caption, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(s.value, style = mt.heading, color = p.tone(s.tone), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (details) Text(s.detail.ifEmpty { " " }, style = mt.caption, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
    }
}
