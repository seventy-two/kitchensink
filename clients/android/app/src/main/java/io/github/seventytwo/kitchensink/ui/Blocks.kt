package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import coil3.compose.AsyncImage
import io.github.seventytwo.kitchensink.draw.DrawScopeShapes
import io.github.seventytwo.kitchensink.draw.Vec2
import io.github.seventytwo.kitchensink.draw.drawWeatherIcon
import io.github.seventytwo.kitchensink.draw.isWeatherIcon
import io.github.seventytwo.kitchensink.model.Align
import io.github.seventytwo.kitchensink.model.Block
import io.github.seventytwo.kitchensink.model.Stat
import io.github.seventytwo.kitchensink.model.Tone
import kotlin.math.ceil

/** One block of a monitor's panel; [monitor] is where a checklist sends its actions. */
@Composable
fun BlockView(monitor: String, b: Block) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    Column(Modifier.fillMaxWidth()) {
        if (b.title.isNotEmpty()) Text(b.title, style = mt.caption, color = p.muted)
        when (b.type) {
            "stats" -> StatsBlock(b.stats)
            "table" -> TableBlock(b)
            "list" -> ListBlock(b)
            "checklist" -> ChecklistBlock(monitor, b)
            else -> if (b.text.isNotEmpty()) Text(b.text, style = mt.body, color = p.text)
        }
    }
}

@Composable
private fun StatsBlock(stats: List<Stat>) {
    if (stats.isEmpty()) return
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    BoxWithConstraints(Modifier.fillMaxWidth().padding(bottom = 4.dp)) {
        val spacing = 12.dp
        val perRow = ((maxWidth + spacing) / (96.dp + spacing)).toInt().coerceIn(1, stats.size)
        val tileW = (maxWidth - spacing * (perRow - 1)) / perRow
        Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
            for (row in stats.chunked(perRow)) {
                Row(horizontalArrangement = Arrangement.spacedBy(spacing)) {
                    for (s in row) {
                        Column(Modifier.width(tileW)) {
                            Text(s.label, style = mt.caption, color = p.muted, maxLines = 1)
                            Row(verticalAlignment = Alignment.Bottom) {
                                Text(s.value, style = mt.big, color = p.tone(s.tone), maxLines = 1)
                                if (isWeatherIcon(s.icon)) {
                                    Spacer(Modifier.width(8.dp))
                                    WeatherIcon(s.icon, 28.dp, Modifier.padding(bottom = 2.dp))
                                }
                            }
                            // A flat series, e.g. no rain at all, says nothing
                            // worth a chart, unless its scale is fixed.
                            val flat = s.series.isEmpty() || s.series.max() == s.series.min()
                            if (!flat || (s.top > 0 && s.series.isNotEmpty())) {
                                Sparkline(s.series, if (s.tone == Tone.None) p.accent else p.tone(s.tone), s.top, Modifier.fillMaxWidth().height(22.dp))
                            }
                            Text(s.detail.ifEmpty { " " }, style = mt.caption, color = p.muted, maxLines = 2)
                        }
                    }
                }
            }
        }
    }
}

@Composable
fun WeatherIcon(name: String, size: Dp, modifier: Modifier = Modifier) {
    val colours = LocalPalette.current.iconColours()
    Canvas(modifier.size(size)) {
        drawWeatherIcon(DrawScopeShapes(this), name, Vec2(0f, 0f), this.size.width, colours)
    }
}

/** A series as a filled line chart, from 0 (or below) to [top], or to the series' highest point when top is 0. */
@Composable
fun Sparkline(v: List<Float>, colour: Color, top: Float, modifier: Modifier) {
    Canvas(modifier) {
        if (v.size < 2) return@Canvas
        val min = minOf(v.min(), 0f)
        val hi = v.max()
        val range = maxOf((if (top > 0) maxOf(top, hi) else hi) - min, 1e-6f)
        val pts = v.mapIndexed { i, y -> Offset(size.width * i / (v.size - 1), size.height - size.height * (y - min) / range) }
        val fill = Path().apply {
            moveTo(0f, size.height)
            pts.forEach { lineTo(it.x, it.y) }
            lineTo(size.width, size.height)
            close()
        }
        drawPath(fill, colour.copy(alpha = 0.18f))
        val line = Path().apply { pts.forEachIndexed { i, o -> if (i == 0) moveTo(o.x, o.y) else lineTo(o.x, o.y) } }
        drawPath(line, colour, style = Stroke(1.5.dp.toPx()))
    }
}

private fun textAlign(a: Align) = when (a) {
    Align.Left -> TextAlign.Start
    Align.Right -> TextAlign.End
    Align.Center -> TextAlign.Center
}

/**
 * A table. The column marked fill takes the spare width, or failing that
 * the left-aligned column with the most text; the rest fit their contents.
 * Too wide for the space, it scrolls sideways.
 */
@Composable
private fun TableBlock(b: Block) {
    val cols = b.columns.size
    if (cols == 0) return
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val actions = LocalActions.current
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current

    var stretch = b.columns.indexOfFirst { it.fill }
    if (stretch < 0) {
        var most = -1
        for (c in 0 until cols) {
            if (b.columns[c].align != Align.Left) continue
            val total = b.rows.sumOf { r -> r.getOrNull(c)?.text?.length ?: 0 }
            if (total > most) { stretch = c; most = total }
        }
    }
    val headers = b.columns.any { it.title.isNotEmpty() }
    fun textW(s: String, style: TextStyle) = if (s.isEmpty()) 0 else measurer.measure(s, style, softWrap = false, maxLines = 1).size.width
    val widths = remember(b, mt) {
        IntArray(cols) { c ->
            var w = textW(b.columns[c].title, mt.caption)
            for (r in b.rows) {
                val cell = r.getOrNull(c) ?: continue
                w = maxOf(w, textW(cell.text, mt.body))
                if (cell.detail.isNotEmpty() && (b.columns[c].align != Align.Left || c != stretch)) {
                    w = maxOf(w, cell.detail.split('\n').maxOf { textW(it, mt.caption) })
                }
            }
            w
        }
    }
    val pad = mt.cellPadH
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val avail = constraints.maxWidth
        val padPx = with(density) { pad.toPx() }.toInt()
        val fixed = widths.withIndex().filter { it.index != stretch }.sumOf { it.value } + padPx * 2 * (cols - 1)
        val minStretch = with(density) { 72.dp.toPx() }.toInt()
        val natural = if (stretch >= 0) widths[stretch] else 0
        val scroll = fixed + minOf(natural, minStretch) > avail
        val stretchW = when {
            stretch < 0 -> 0
            scroll -> minOf(natural, with(density) { 220.dp.toPx() }.toInt())
            else -> avail - fixed
        }
        fun colW(c: Int): Dp = with(density) { (if (c == stretch) stretchW else ceil(widths[c].toFloat()).toInt()).toDp() }
        val scrollState = rememberScrollState()
        Column(if (scroll) Modifier.horizontalScroll(scrollState) else Modifier.fillMaxWidth()) {
            @Composable
            fun row(bg: Color, content: @Composable (Int) -> Unit) {
                Row(Modifier.background(bg).padding(vertical = mt.cellPadV)) {
                    for (c in 0 until cols) {
                        val start = if (c == 0) 0.dp else pad
                        val end = if (c == cols - 1) 0.dp else pad
                        Box(Modifier.padding(start = start, end = end).width(colW(c))) { content(c) }
                    }
                }
            }
            if (headers) row(Color.Transparent) { c ->
                Text(b.columns[c].title, style = mt.caption, color = p.muted, textAlign = textAlign(b.columns[c].align), modifier = Modifier.fillMaxWidth(), maxLines = 1)
            }
            for ((i, r) in b.rows.withIndex()) {
                row(if (i % 2 == 1) p.rowAlt else Color.Transparent) { c ->
                    val cell = r.getOrNull(c) ?: return@row
                    val align = textAlign(b.columns[c].align)
                    Column(Modifier.fillMaxWidth()) {
                        val link = cell.url.isNotEmpty()
                        Text(
                            cell.text,
                            style = mt.body.copy(textDecoration = if (link) TextDecoration.Underline else null),
                            color = p.tone(cell.tone),
                            textAlign = align,
                            modifier = Modifier.fillMaxWidth().then(if (link) Modifier.tappable { actions.open(cell.url) } else Modifier),
                        )
                        if (cell.detail.isNotEmpty()) Text(cell.detail, style = mt.caption, color = p.muted, textAlign = align, modifier = Modifier.fillMaxWidth())
                    }
                }
            }
        }
    }
}

@Composable
private fun ListBlock(b: Block) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val actions = LocalActions.current
    Column(Modifier.fillMaxWidth()) {
        for (it in b.items) {
            Row(
                Modifier.fillMaxWidth().then(if (it.url.isNotEmpty()) Modifier.tappable { actions.open(it.url) } else Modifier).padding(6.dp),
            ) {
                if (it.image.isNotEmpty()) {
                    // Cropped to the thumbnail's shape rather than squashed.
                    AsyncImage(
                        it.image, null, contentScale = ContentScale.Crop,
                        modifier = Modifier.size(112.dp, 84.dp).background(p.frame).clipToBounds(),
                    )
                    Spacer(Modifier.width(12.dp))
                }
                Column(Modifier.weight(1f)) {
                    Text(it.title, style = mt.heading, color = p.text)
                    if (it.subtitle.isNotEmpty()) Text(it.subtitle, style = mt.body, color = if (it.tone == Tone.None) p.text else p.tone(it.tone))
                    if (it.detail.isNotEmpty()) Text(it.detail, style = mt.caption, color = p.muted)
                }
            }
        }
    }
}

/**
 * Items with checkboxes, a delete button on each, and a box for new items
 * if the block takes them. Each change goes to the monitor as an action;
 * ticks show at once, ahead of the server's snapshot.
 */
@Composable
private fun ChecklistBlock(monitor: String, b: Block) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val actions = LocalActions.current
    val state = LocalPanelState.current
    Column(Modifier.fillMaxWidth()) {
        for (it in b.items) {
            val done = state.ticked["$monitor/${it.id}"] ?: it.done
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Row(Modifier.weight(1f).tappable { actions.toggle(monitor, it.id, !done) }, verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(
                        done, null, Modifier.padding(4.dp).size(28.dp),
                        colors = CheckboxDefaults.colors(checkedColor = p.accent, uncheckedColor = p.muted, checkmarkColor = p.card),
                    )
                    Text(
                        it.title, style = mt.body.copy(textDecoration = if (done) TextDecoration.LineThrough else null),
                        color = if (done) p.muted else p.text, modifier = Modifier.padding(start = 6.dp, end = 4.dp),
                    )
                }
                IconButtonSmall(Icons.Filled.Close, "Delete", size = 32.dp, iconSize = 16.dp) { actions.delete(monitor, it.id) }
            }
        }
        if (b.items.isEmpty()) Text("Nothing to do.", style = mt.body, color = p.muted)
        if (b.addHint.isNotEmpty()) {
            val drafts = state.drafts
            var local by remember { mutableStateOf("") }
            val text = drafts?.get(monitor) ?: local
            fun set(v: String) = if (drafts != null) drafts[monitor] = v else local = v
            Spacer(Modifier.height(4.dp))
            BasicTextField(
                text, ::set,
                textStyle = mt.body.copy(color = p.text),
                singleLine = true,
                cursorBrush = SolidColor(p.accent),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                // Stays in the box for the next one.
                keyboardActions = KeyboardActions(onDone = {
                    actions.add(monitor, text)
                    set("")
                }),
                modifier = Modifier.fillMaxWidth(),
                decorationBox = { inner ->
                    Box(Modifier.fillMaxWidth().background(p.frame).padding(horizontal = 10.dp, vertical = 8.dp)) {
                        if (text.isEmpty()) Text(b.addHint, style = mt.body, color = p.muted)
                        inner()
                    }
                },
            )
        }
    }
}
