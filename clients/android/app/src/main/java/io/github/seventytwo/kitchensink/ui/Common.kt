package io.github.seventytwo.kitchensink.ui

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Icon
import androidx.compose.material3.ripple
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.draw.IconColours

/** What the panels can do: everything that changes something goes through here. */
interface Actions {
    fun open(url: String)
    fun refresh(monitor: String)
    fun toggle(monitor: String, id: String, done: Boolean)
    fun delete(monitor: String, id: String)
    fun add(monitor: String, text: String)

    object None : Actions {
        override fun open(url: String) {}
        override fun refresh(monitor: String) {}
        override fun toggle(monitor: String, id: String, done: Boolean) {}
        override fun delete(monitor: String, id: String) {}
        override fun add(monitor: String, text: String) {}
    }
}

/** What the panels show that isn't in a snapshot: ticks not yet confirmed, refreshes under way, drafts of new to-dos. */
data class PanelState(
    val ticked: Map<String, Boolean> = emptyMap(),
    val refreshing: Set<String> = emptySet(),
    val now: Long = 0,
    val drafts: SnapshotStateMap<String, String>? = null,
)

val LocalActions = staticCompositionLocalOf<Actions> { Actions.None }
val LocalPanelState = staticCompositionLocalOf { PanelState() }

/** Whether the UI is driven by a remote's D-pad, so focus needs to show clearly. */
val LocalTv = staticCompositionLocalOf { false }

/** Opens an http or https link in the browser. Other schemes are ignored, since links come from the server. */
fun openUrl(context: Context, url: String) {
    if (!url.startsWith("https://") && !url.startsWith("http://")) return
    try {
        context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    } catch (_: ActivityNotFoundException) {
        // No browser, e.g. on a TV.
    }
}

/**
 * Makes something tappable, and focusable from a remote, with an accent
 * outline while it has focus.
 */
@Composable
fun Modifier.tappable(enabled: Boolean = true, shape: Shape = RectangleShape, onClick: () -> Unit): Modifier {
    val source = remember { MutableInteractionSource() }
    val focused by source.collectIsFocusedAsState()
    val p = LocalPalette.current
    return this
        .then(if (focused) Modifier.border(2.dp, p.accent, shape) else Modifier)
        .clickable(source, ripple(), enabled = enabled, onClick = onClick)
}

/** A small flat icon button, muted until it has focus. */
@Composable
fun IconButtonSmall(
    icon: ImageVector,
    description: String,
    modifier: Modifier = Modifier,
    size: Dp = 36.dp,
    iconSize: Dp = 18.dp,
    tint: Color = LocalPalette.current.muted,
    enabled: Boolean = true,
    onClick: () -> Unit,
) {
    Box(modifier.size(size).tappable(enabled, onClick = onClick), contentAlignment = Alignment.Center) {
        Icon(icon, description, Modifier.size(iconSize), tint = if (enabled) tint else tint.copy(alpha = 0.4f))
    }
}

fun Palette.iconColours(): IconColours {
    fun mix(a: Color, b: Color, k: Float) = Color(a.red + (b.red - a.red) * k, a.green + (b.green - a.green) * k, a.blue + (b.blue - a.blue) * k)
    return IconColours(warn, text, mix(text, muted, 0.3f), mix(muted, frame, 0.2f), accent, text, warn)
}

/** An eye, for showing and hiding private figures. */
val EyeIcon: ImageVector by lazy {
    ImageVector.Builder("Eye", 24.dp, 24.dp, 24f, 24f).apply {
        path(stroke = SolidColor(Color.Black), strokeLineWidth = 1.8f, strokeLineCap = StrokeCap.Round) {
            moveTo(2f, 12f)
            quadTo(12f, 2f, 22f, 12f)
            quadTo(12f, 22f, 2f, 12f)
            close()
        }
        path(fill = SolidColor(Color.Black)) {
            moveTo(15.2f, 12f)
            arcTo(3.2f, 3.2f, 0f, isMoreThanHalf = true, isPositiveArc = true, x1 = 8.8f, y1 = 12f)
            arcTo(3.2f, 3.2f, 0f, isMoreThanHalf = true, isPositiveArc = true, x1 = 15.2f, y1 = 12f)
            close()
        }
    }.build()
}

/** The eye struck through, while private figures are hidden. */
val EyeOffIcon: ImageVector by lazy {
    ImageVector.Builder("EyeOff", 24.dp, 24.dp, 24f, 24f).apply {
        path(stroke = SolidColor(Color.Black), strokeLineWidth = 1.8f, strokeLineCap = StrokeCap.Round) {
            moveTo(2f, 12f)
            quadTo(12f, 2f, 22f, 12f)
            quadTo(12f, 22f, 2f, 12f)
            close()
            moveTo(4f, 4f)
            lineTo(20f, 20f)
        }
    }.build()
}
