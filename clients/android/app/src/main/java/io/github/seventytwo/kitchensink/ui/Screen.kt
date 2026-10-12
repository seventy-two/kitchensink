package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.data.Config
import io.github.seventytwo.kitchensink.model.Event
import io.github.seventytwo.kitchensink.model.Pin
import io.github.seventytwo.kitchensink.model.Snapshot
import io.github.seventytwo.kitchensink.model.arrange
import io.github.seventytwo.kitchensink.model.feedBeside
import io.github.seventytwo.kitchensink.model.hasPrivate
import io.github.seventytwo.kitchensink.model.oledDim
import io.github.seventytwo.kitchensink.model.oledShift
import io.github.seventytwo.kitchensink.model.oledSwapSides
import io.github.seventytwo.kitchensink.model.useCompact
import io.github.seventytwo.kitchensink.model.withoutPrivate
import io.github.seventytwo.kitchensink.net.ConnState
import kotlinx.coroutines.launch

/** Everything the dashboard shows, so it can be drawn from tests as well as from the app. */
data class ScreenState(
    val config: Config,
    val snapshots: List<Snapshot>,
    val events: List<Event>,
    val conn: ConnState,
    val error: String,
    /** Unix seconds. */
    val now: Long,
    /** When there was last a touch or key press (Unix seconds), for dimming. */
    val lastActivity: Long,
    /** Until when something new keeps the screen from dimming. */
    val wakeUntil: Long,
    /** Whether this is TV mode: a dashboard that stays on, driven by a remote. */
    val tv: Boolean = false,
    /** Whether to leave a margin for a TV's overscan. */
    val overscan: Boolean = false,
    /** Seconds since start, for the cat. */
    val clock: () -> Double = { 0.0 },
)

/** What the dashboard can ask of the app, besides [Actions]. */
interface ScreenCallbacks {
    fun pin(monitor: String, pin: Pin)
    fun clear(id: Long)
    fun clearAll()
    fun togglePrivate()
    fun openSettings()
    fun titleOf(monitor: String): String

    object None : ScreenCallbacks {
        override fun pin(monitor: String, pin: Pin) {}
        override fun clear(id: Long) {}
        override fun clearAll() {}
        override fun togglePrivate() {}
        override fun openSettings() {}
        override fun titleOf(monitor: String) = monitor
    }
}

/** The panels in the user's order, private figures left out if they're hidden. */
fun orderedPanels(snapshots: List<Snapshot>, config: Config): List<Snapshot> {
    val shown = if (config.hidePrivate) snapshots.map { it.withoutPrivate() } else snapshots
    if (config.order.isEmpty()) return shown
    val byId = shown.associateBy { it.monitor }
    return arrange(shown.map { it.monitor }, config.order).mapNotNull { byId[it] }
}

@Composable
fun DashboardScreen(state: ScreenState, cb: ScreenCallbacks) {
    val c = state.config
    val p = LocalPalette.current
    BoxWithConstraints(Modifier.fillMaxSize().background(p.bg)) {
        // OLED care moves everything round a small circle, inside a margin
        // left for it.
        val margin = if (c.oled.shift) 6.dp else 0.dp
        val (dx, dy) = if (c.oled.shift) oledShift(state.now, 6f) else 0f to 0f
        val over = if (state.overscan) Modifier.padding(horizontal = 40.dp, vertical = 20.dp) else Modifier
        var compact by remember { mutableStateOf(true) }
        compact = useCompact(compact, maxWidth.value, maxHeight.value)
        val metrics = Metrics.of(compact)
        KitchensinkTheme(p, metrics) {
            Column(over.padding(margin).offset(dx.dp, dy.dp).fillMaxSize().padding(horizontal = if (state.overscan) 0.dp else 10.dp)) {
                Header(state, cb)
                Box(Modifier.weight(1f).fillMaxWidth()) { Body(state, cb, compact) }
            }
        }
        // OLED care dims everything when there's been no touch or key press
        // for a while; it's back as soon as there is.
        if (c.oled.dim && oledDim(state.now, state.lastActivity, state.wakeUntil)) {
            Box(Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.6f)))
        }
    }
}

@Composable
private fun Header(state: ScreenState, cb: ScreenCallbacks) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val (status, dot) = when (state.conn) {
        ConnState.NotConfigured -> "Not set up" to p.muted
        ConnState.Connecting -> "Connecting…" to p.warn
        ConnState.Connected -> "Live" to p.good
        ConnState.Offline -> (if (state.error.isEmpty()) "Offline" else "Offline: ${state.error}") to p.bad
    }
    Row(Modifier.fillMaxWidth().height(if (mt.gap < 12.dp) 44.dp else 52.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("kitchensink", style = mt.title, color = p.text)
        Spacer(Modifier.width(14.dp))
        Canvas(Modifier.size(8.dp)) { drawCircle(dot) }
        Spacer(Modifier.width(6.dp))
        Text(status, style = mt.caption, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        // Hide or show private figures, e.g. a portfolio: only there when a
        // monitor sends some.
        if (state.snapshots.any { it.hasPrivate() }) {
            val hidden = state.config.hidePrivate
            IconButtonSmall(
                if (hidden) EyeOffIcon else EyeIcon, if (hidden) "Show private figures" else "Hide private figures",
                size = 40.dp, iconSize = 20.dp, tint = if (hidden) p.muted else p.text, onClick = cb::togglePrivate,
            )
        }
        IconButtonSmall(Icons.Filled.Settings, "Settings", size = 40.dp, iconSize = 20.dp, tint = p.text, onClick = cb::openSettings)
    }
}

@Composable
private fun Empty(message: String, detail: String, settings: Boolean, cb: ScreenCallbacks) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    Column(Modifier.fillMaxSize().padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.Center) {
        Text(message, style = mt.body, color = p.muted, textAlign = TextAlign.Center)
        if (detail.isNotEmpty()) Text(detail, style = mt.caption, color = p.muted, textAlign = TextAlign.Center)
        if (settings) {
            Spacer(Modifier.height(12.dp))
            Box(Modifier.background(p.accent).tappable(onClick = cb::openSettings).padding(horizontal = 16.dp, vertical = 10.dp)) {
                Text("Open settings", style = mt.heading, color = Color.White)
            }
        }
    }
}

@Composable
private fun Body(state: ScreenState, cb: ScreenCallbacks, compact: Boolean) {
    val c = state.config
    when {
        !c.configured -> return Empty("Add your server's address and token to get started.", "", true, cb)
        state.snapshots.isEmpty() -> {
            val offline = state.conn == ConnState.Offline
            return Empty(if (offline) "Can't reach the server." else "Connecting…", if (offline) state.error else "", offline, cb)
        }
    }
    val panels = orderedPanels(state.snapshots, c)
    val pins = buildMap {
        c.collapsed.forEach { put(it, Pin.Closed) }
        c.expanded.forEach { put(it, Pin.Open) }
    }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        val gap = LocalMetrics.current.gap
        val height = maxHeight
        if (feedBeside(maxWidth.value)) {
            val feedW = if (compact) 300.dp else 340.dp
            // OLED care swaps the side column's side every other hour.
            val feedFirst = c.oled.swap && oledSwapSides(state.now)
            Row(Modifier.fillMaxSize(), horizontalArrangement = Arrangement.spacedBy(gap)) {
                if (feedFirst) FeedColumn(state, cb, Modifier.width(feedW))
                PanelsColumn(panels, pins, compact, height, cb, Modifier.weight(1f))
                if (!feedFirst) FeedColumn(state, cb, Modifier.width(feedW))
            }
        } else {
            Pages(state, cb, panels, pins, compact, height)
        }
    }
}

@Composable
private fun FeedColumn(state: ScreenState, cb: ScreenCallbacks, modifier: Modifier, heading: Boolean = true) {
    Feed(
        state.events, cb::titleOf, state.now, cb::clear, cb::clearAll, modifier,
        cat = state.config.cat, catTime = state.clock, heading = heading,
    )
}

@Composable
private fun PanelsColumn(panels: List<Snapshot>, pins: Map<String, Pin>, compact: Boolean, height: Dp, cb: ScreenCallbacks, modifier: Modifier) {
    val scroll = rememberScrollState()
    Box(modifier.verticalScroll(scroll)) {
        if (compact) CompactPanels(panels, pins, height, cb::pin, Modifier.fillMaxWidth())
        else MasonryPanels(panels, Modifier.fillMaxWidth())
    }
}

/** On a narrow screen, the panels and Recent are pages side by side, swiped between or picked by their tabs. */
@Composable
private fun Pages(state: ScreenState, cb: ScreenCallbacks, panels: List<Snapshot>, pins: Map<String, Pin>, compact: Boolean, height: Dp) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    val pager = rememberPagerState { 2 }
    val scope = rememberCoroutineScope()
    // A dot on Recent's tab for events that came since it was last seen.
    var seen by remember { mutableLongStateOf(state.events.firstOrNull()?.id ?: 0) }
    if (pager.currentPage == 1) seen = state.events.firstOrNull()?.id ?: seen
    val unseen = (state.events.firstOrNull()?.id ?: 0) > seen
    val tabH = 36.dp
    Column(Modifier.fillMaxSize()) {
        Row(Modifier.fillMaxWidth().height(tabH)) {
            for ((i, name) in listOf("Dashboard", "Recent").withIndex()) {
                val on = pager.currentPage == i
                Box(Modifier.weight(1f).height(tabH).tappable { scope.launch { pager.animateScrollToPage(i) } }, contentAlignment = Alignment.Center) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(name, style = mt.heading, color = if (on) p.text else p.muted)
                        if (i == 1 && unseen) {
                            Spacer(Modifier.width(6.dp))
                            Canvas(Modifier.size(6.dp)) { drawCircle(p.accent) }
                        }
                    }
                    if (on) Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth(0.5f).height(2.dp).background(p.accent))
                }
            }
        }
        Spacer(Modifier.height(6.dp))
        HorizontalPager(pager, Modifier.weight(1f).fillMaxWidth(), pageSpacing = 16.dp, beyondViewportPageCount = 1) { page ->
            if (page == 0) PanelsColumn(panels, pins, compact, height - tabH - 6.dp, cb, Modifier.fillMaxSize())
            else FeedColumn(state, cb, Modifier.fillMaxSize(), heading = false)
        }
    }
}
