package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.model.Fit
import io.github.seventytwo.kitchensink.model.Panel
import io.github.seventytwo.kitchensink.model.Pin
import io.github.seventytwo.kitchensink.model.Show
import io.github.seventytwo.kitchensink.model.Snapshot
import io.github.seventytwo.kitchensink.model.ago
import io.github.seventytwo.kitchensink.model.fitPanels
import io.github.seventytwo.kitchensink.model.repin

private val Slot = 36.dp

@Composable
private fun CardBox(modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val p = LocalPalette.current
    Box(modifier.fillMaxWidth().background(p.card).border(1.dp, p.border)) { content() }
}

/** The panel's subtitle, error and blocks. */
@Composable
private fun PanelBody(s: Snapshot, wrapSubtitle: Boolean = true) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (wrapSubtitle && s.subtitle.isNotEmpty()) Text(s.subtitle, style = mt.caption, color = p.muted)
        if (s.error.isNotEmpty()) ErrorLine(s.error)
        if (s.blocks.isEmpty() && s.error.isEmpty()) Text("Waiting for the first check…", style = mt.body, color = p.muted)
        for (b in s.blocks) BlockView(s.monitor, b)
    }
}

@Composable
private fun ErrorLine(error: String) {
    val p = LocalPalette.current
    Row(verticalAlignment = Alignment.Top) {
        Icon(Icons.Filled.Warning, null, Modifier.padding(top = 2.dp).width(14.dp), tint = p.warn)
        Spacer(Modifier.width(6.dp))
        Text(error, style = LocalMetrics.current.caption, color = p.warn)
    }
}

@Composable
private fun RefreshButton(s: Snapshot) {
    val actions = LocalActions.current
    val pending = s.monitor in LocalPanelState.current.refreshing
    IconButtonSmall(Icons.Filled.Refresh, if (pending) "Checking…" else "Check now", size = Slot, enabled = !pending) {
        actions.refresh(s.monitor)
    }
}

/**
 * One monitor in the compact layout: collapsed, a row of its summary;
 * open, its full panel, with a strip down the right holding the close
 * chevron and refresh. A clipped panel scrolls within [body]. [onToggle]
 * opens or closes it; it's null while the card is only being measured.
 */
@Composable
fun CompactCard(s: Snapshot, show: Show, body: Dp, onToggle: (() -> Unit)?) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val state = LocalPanelState.current
    val toggle = onToggle ?: {}
    CardBox {
        if (show == Show.Row) {
            val failed = s.error.isNotEmpty()
            val pending = s.monitor in state.refreshing
            val whenText = when {
                failed -> "Failed"
                pending -> "refreshing…"
                s.updated == 0L -> "waiting…"
                else -> ""
            }
            Column(Modifier.fillMaxWidth().tappable(onClick = toggle).padding(start = mt.cardPadH, end = 4.dp, top = mt.cardPadV, bottom = mt.cardPadV)) {
                @Composable
                fun right() {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (whenText.isNotEmpty()) {
                            if (failed) Icon(Icons.Filled.Warning, null, Modifier.width(13.dp), tint = p.warn)
                            Text(whenText, style = mt.caption, color = if (failed) p.warn else p.muted, modifier = Modifier.padding(start = 4.dp, end = 4.dp))
                        }
                        Icon(Icons.Filled.KeyboardArrowDown, "Open", Modifier.width(20.dp), tint = p.muted)
                    }
                }
                @Composable
                fun summary(modifier: Modifier) {
                    Box(modifier) {
                        when {
                            s.summary.isNotEmpty() -> Summary(s.summary, s.summaryStyle)
                            s.blocks.isEmpty() && !failed -> Text("Waiting for the first check…", style = mt.body, color = p.muted)
                            else -> Text("Tap for details", style = mt.body, color = p.muted)
                        }
                    }
                }
                if (s.subtitle.isNotEmpty()) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(s.subtitle, style = mt.caption, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                        right()
                    }
                    summary(Modifier.fillMaxWidth().padding(end = 10.dp, top = 2.dp))
                } else {
                    Row(verticalAlignment = Alignment.Top) {
                        summary(Modifier.weight(1f).padding(end = 8.dp))
                        right()
                    }
                }
            }
        } else {
            val clip = show == Show.Clipped
            Row(Modifier.fillMaxWidth().padding(start = mt.cardPadH, top = mt.cardPadV, bottom = mt.cardPadV)) {
                val scroll = rememberScrollState()
                Box(Modifier.weight(1f).then(if (clip) Modifier.height(body).verticalScroll(scroll) else Modifier)) {
                    PanelBody(s)
                }
                // The strip closes the panel when tapped.
                Column(
                    Modifier.width(Slot + 8.dp).then(if (clip) Modifier.height(body) else Modifier),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    IconButtonSmall(Icons.Filled.KeyboardArrowUp, "Close", size = Slot, iconSize = 22.dp, onClick = toggle)
                    RefreshButton(s)
                    if (s.timely && s.updated > 0) {
                        Text(ago(s.updated, state.now).removeSuffix(" ago"), style = mt.caption, color = p.muted, maxLines = 1)
                    }
                }
            }
        }
    }
}

/** Keeps what the last layout measured, for working out a tap's new pin. */
private class Measured {
    var panels: List<Panel> = emptyList()
    var fit = Fit(emptyList())
}

/**
 * Fills the space with one row per monitor, full width so the summaries
 * line up, opening as many full panels as fit [viewport] (see fitPanels).
 * Tapping a row opens or closes it, and [onPin] keeps that choice while it
 * differs from what would happen by itself.
 */
@Composable
fun CompactPanels(
    panels: List<Snapshot>,
    pins: Map<String, Pin>,
    viewport: Dp,
    onPin: (String, Pin) -> Unit,
    modifier: Modifier = Modifier,
) {
    val mt = LocalMetrics.current
    val density = LocalDensity.current
    val measured = remember { Measured() }
    val gapPx = with(density) { mt.gap.toPx() }
    val heightPx = with(density) { viewport.toPx() }
    // A panel only takes the space left over if it shows something useful
    // there: a table's header and four rows.
    val minBody = with(density) { (mt.body.lineHeight.toDp() + mt.cellPadV * 2) * 5 }.let { with(density) { it.toPx() } }

    fun tapped(i: Int) {
        val ps = measured.panels
        if (i >= ps.size || i >= panels.size) return
        val open = measured.fit.show[i] == Show.Row
        onPin(panels[i].monitor, repin(ps, i, open, heightPx, gapPx, minBody))
    }

    SubcomposeLayout(modifier) { constraints ->
        val w = constraints.maxWidth
        val cw = Constraints(minWidth = w, maxWidth = w)
        // Each monitor measured each way at this width.
        val rows = panels.mapIndexed { i, s -> subcompose(s.monitor + "#row") { CompactCard(s, Show.Row, 0.dp) { tapped(i) } }.first().measure(cw) }
        val fulls = panels.mapIndexed { i, s -> subcompose(s.monitor + "#full") { CompactCard(s, Show.Full, 0.dp) { tapped(i) } }.first().measure(cw) }
        val clips = panels.map { s -> subcompose(s.monitor + "#clip0") { CompactCard(s, Show.Clipped, 0.dp, null) }.first().measure(cw) }
        val ps = panels.mapIndexed { i, s ->
            Panel(rows[i].height.toFloat(), fulls[i].height.toFloat(), (clips[i].height - rows[i].height).toFloat(), pins[s.monitor] ?: Pin.Auto)
        }
        val fit = fitPanels(ps, heightPx, gapPx, minBody)
        measured.panels = ps
        measured.fit = fit
        // The row and full copies are the real thing; only a clipped one,
        // which depends on the space left, is made again.
        val bodyDp = fit.body.toDp()
        val placed = panels.mapIndexed { i, s ->
            when (fit.show[i]) {
                Show.Row -> rows[i]
                Show.Full -> fulls[i]
                Show.Clipped -> subcompose(s.monitor + "#clip") { CompactCard(s, Show.Clipped, bodyDp) { tapped(i) } }.first().measure(cw)
            }
        }
        val gap = gapPx.toInt()
        val total = placed.sumOf { it.height } + gap * maxOf(0, placed.size - 1)
        layout(w, total) {
            var y = 0
            for (pl in placed) {
                pl.place(0, y)
                y += pl.height + gap
            }
        }
    }
}

/** One monitor's full panel, for screens with room for them all. */
@Composable
fun FullCard(s: Snapshot) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val state = LocalPanelState.current
    CardBox {
        Column(Modifier.padding(start = mt.cardPadH, end = 6.dp, top = 6.dp, bottom = mt.cardPadV)) {
            // What's being watched on the left; when it was updated (if that
            // matters now) and refresh on the right.
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(s.subtitle, style = mt.caption, color = p.muted, modifier = Modifier.weight(1f))
                val pending = s.monitor in state.refreshing
                val whenText = if (pending) "refreshing…" else if (s.timely && s.updated > 0) "updated " + ago(s.updated, state.now) else ""
                if (whenText.isNotEmpty()) Text(whenText, style = mt.caption, color = p.muted)
                RefreshButton(s)
            }
            Box(Modifier.padding(end = mt.cardPadH - 6.dp)) { PanelBody(s, wrapSubtitle = false) }
        }
    }
}

/** Full panels in columns about 440dp wide, each going in the shortest column so far. */
@Composable
fun MasonryPanels(panels: List<Snapshot>, modifier: Modifier = Modifier) {
    val gap = LocalMetrics.current.gap
    Layout(content = { for (s in panels) Box { FullCard(s) } }, modifier = modifier) { measurables, constraints ->
        val g = gap.roundToPx()
        val width = constraints.maxWidth
        val cols = maxOf(1, (width + g) / (440.dp.roundToPx() + g)).coerceAtMost(maxOf(1, measurables.size))
        val colW = (width - g * (cols - 1)) / cols
        val heights = IntArray(cols)
        val places = measurables.map { m ->
            val pl = m.measure(Constraints.fixedWidth(colW))
            val c = heights.indices.minBy { heights[it] }
            val at = Triple(pl, c * (colW + g), heights[c])
            heights[c] += pl.height + g
            at
        }
        layout(width, maxOf(0, (heights.maxOrNull() ?: 0) - g)) {
            for ((pl, x, y) in places) pl.place(x, y)
        }
    }
}

