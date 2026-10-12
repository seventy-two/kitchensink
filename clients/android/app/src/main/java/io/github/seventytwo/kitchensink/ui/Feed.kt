package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Text
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameMillis
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.draw.DrawScopeShapes
import io.github.seventytwo.kitchensink.draw.Vec2
import io.github.seventytwo.kitchensink.draw.catAt
import io.github.seventytwo.kitchensink.draw.drawCat
import io.github.seventytwo.kitchensink.model.Event
import io.github.seventytwo.kitchensink.model.Priority
import io.github.seventytwo.kitchensink.model.ago
import kotlinx.coroutines.delay

/** The Recent column: a heading with clear-all, then the events, newest first. */
@Composable
fun Feed(
    events: List<Event>,
    titleOf: (String) -> String,
    now: Long,
    onClear: (Long) -> Unit,
    onClearAll: () -> Unit,
    modifier: Modifier = Modifier,
    cat: Boolean = false,
    catTime: (() -> Double)? = null,
    /** Whether to say "Recent"; not on a page whose tab already does. */
    heading: Boolean = true,
) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val actions = LocalActions.current
    Column(modifier) {
        if (cat && catTime != null) Cat(catTime)
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(if (heading) "Recent" else "", style = mt.heading, color = p.text, modifier = Modifier.weight(1f).padding(vertical = 6.dp))
            if (events.isNotEmpty()) IconButtonSmall(Icons.Filled.Close, "Clear all", size = 32.dp, onClick = onClearAll)
        }
        if (events.isEmpty()) {
            Text("Nothing new.", style = mt.body, color = p.muted)
            return@Column
        }
        LazyColumn(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(1.dp)) {
            items(events.take(150), key = { it.id }) { e ->
                val swipe = rememberSwipeToDismissBoxState()
                LaunchedEffect(swipe.currentValue) {
                    if (swipe.currentValue != SwipeToDismissBoxValue.Settled) onClear(e.id)
                }
                SwipeToDismissBox(swipe, backgroundContent = { Box(Modifier.fillMaxWidth().fillMaxHeight().background(p.frame)) }) {
                    EventCard(e, titleOf(e.monitor), now, onClear = { onClear(e.id) }, onOpen = { actions.open(e.url) })
                }
            }
        }
    }
}

@Composable
private fun EventCard(e: Event, monitor: String, now: Long, onClear: () -> Unit, onOpen: () -> Unit) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val pad = if (mt.gap < 12.dp) 8.dp else 10.dp
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min).background(p.card)) {
        if (e.priority == Priority.High && !e.quiet) Box(Modifier.width(3.dp).fillMaxHeight().background(p.accent))
        Column(
            Modifier.weight(1f).then(if (e.url.isNotEmpty()) Modifier.tappable(onClick = onOpen) else Modifier).padding(start = pad, top = pad * 0.8f, bottom = pad * 0.8f),
        ) {
            Text("$monitor  ·  ${ago(e.time, now)}", style = mt.caption, color = p.muted)
            Text(e.title, style = if (e.quiet) mt.body else mt.heading, color = if (e.quiet) p.muted else p.text)
            if (e.body.isNotEmpty()) Text(e.body, style = mt.caption, color = p.muted)
        }
        IconButtonSmall(Icons.Filled.Close, "Clear", size = 32.dp, iconSize = 14.dp, onClick = onClear)
    }
}

/** The black cat, asleep along the top of the Recent column; see catAt. */
@Composable
private fun Cat(time: () -> Double) {
    var t by remember { mutableDoubleStateOf(time()) }
    val asleep = remember { FloatArray(1) { 1f } }
    LaunchedEffect(Unit) {
        while (true) {
            // Asleep, her breathing and Z's are slow: ten frames a second will do.
            delay(if (asleep[0] >= 1) 100 else 33)
            withFrameMillis { }
            t = time()
        }
    }
    val size = 1.1f
    // Her space is 68 units tall: 64 above the floor (an arched tail), 4
    // below (rounded paws and her rim).
    Canvas(Modifier.fillMaxWidth().height((68 * size).dp)) {
        val px = size * density
        val c = catAt(t, this.size.width / 2 / px)
        asleep[0] = c.asleep
        drawCat(DrawScopeShapes(this), c, t, Vec2(this.size.width / 2, this.size.height - 4 * px), px)
    }
}
