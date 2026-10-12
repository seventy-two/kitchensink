package io.github.seventytwo.kitchensink.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.RadioButton
import androidx.compose.material3.RadioButtonDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import io.github.seventytwo.kitchensink.data.Config
import io.github.seventytwo.kitchensink.data.OledCare
import io.github.seventytwo.kitchensink.data.ThemeChoice
import io.github.seventytwo.kitchensink.data.TvMode
import io.github.seventytwo.kitchensink.model.Priority
import io.github.seventytwo.kitchensink.model.Snapshot
import io.github.seventytwo.kitchensink.model.shift

/** What the settings need from the device, which the screen can't find out for itself. */
data class DeviceInfo(
    val isTv: Boolean = false,
    /** Whether Android lets the app run in the background without being stopped to save battery. */
    val unrestricted: Boolean = true,
    val canNotify: Boolean = true,
)

/**
 * The settings, edited as a draft and kept on Save, like the Windows
 * client's dialog. Cancel (or back) leaves them as they were, unless
 * nothing's set up yet.
 */
@Composable
fun SettingsScreen(
    config: Config,
    panels: List<Snapshot>,
    device: DeviceInfo,
    onSave: (Config) -> Unit,
    onCancel: () -> Unit,
    onUnrestrict: () -> Unit,
    onAskNotify: () -> Unit,
) {
    val p = LocalPalette.current
    val mt = LocalMetrics.current
    var draft by remember { mutableStateOf(config) }
    var showToken by remember { mutableStateOf(false) }
    val fieldColours = OutlinedTextFieldDefaults.colors(
        focusedTextColor = p.text, unfocusedTextColor = p.text, focusedBorderColor = p.accent, unfocusedBorderColor = p.border,
        cursorColor = p.accent, focusedContainerColor = p.frame, unfocusedContainerColor = p.frame,
    )

    @Composable
    fun heading(text: String) {
        Spacer(Modifier.height(14.dp))
        Text(text, style = mt.caption, color = p.muted)
        Spacer(Modifier.height(2.dp))
    }

    @Composable
    fun toggle(label: String, on: Boolean, what: String = "", enabled: Boolean = true, set: (Boolean) -> Unit) {
        Row(
            Modifier.fillMaxWidth().tappable(enabled) { set(!on) }.padding(vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(Modifier.weight(1f)) {
                Text(label, style = mt.body, color = if (enabled) p.text else p.muted)
                if (what.isNotEmpty()) Text(what, style = mt.caption, color = p.muted)
            }
            Switch(
                on, null, enabled = enabled,
                colors = SwitchDefaults.colors(checkedTrackColor = p.accent, checkedThumbColor = Color.White, uncheckedTrackColor = p.frame, uncheckedThumbColor = p.muted, uncheckedBorderColor = p.border),
            )
        }
    }

    @Composable
    fun <T> choice(options: List<Pair<T, String>>, value: T, enabled: Boolean = true, set: (T) -> Unit) {
        for ((v, label) in options) {
            Row(Modifier.fillMaxWidth().tappable(enabled) { set(v) }.padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                RadioButton(v == value, null, enabled = enabled, colors = RadioButtonDefaults.colors(selectedColor = p.accent, unselectedColor = p.muted))
                Text(label, style = mt.body, color = if (enabled) p.text else p.muted, modifier = Modifier.padding(start = 8.dp))
            }
        }
    }

    @Composable
    fun button(label: String, primary: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
        Box(
            modifier.background(if (primary) p.accent else p.frame).tappable(onClick = onClick).padding(horizontal = 18.dp, vertical = 10.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(label, style = mt.heading, color = if (primary) Color.White else p.text)
        }
    }

    Box(Modifier.fillMaxSize().background(p.bg), contentAlignment = Alignment.TopCenter) {
        Column(Modifier.widthIn(max = 560.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(20.dp)) {
            Text("Settings", style = mt.title, color = p.text)
            heading("Server address")
            OutlinedTextField(
                draft.url, { draft = draft.copy(url = it) }, Modifier.fillMaxWidth(), singleLine = true,
                placeholder = { Text("https://example.com/kitchensink", color = p.muted) },
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), colors = fieldColours, textStyle = mt.body,
            )
            heading("Token")
            OutlinedTextField(
                draft.token, { draft = draft.copy(token = it.trim()) }, Modifier.fillMaxWidth(), singleLine = true,
                visualTransformation = if (showToken) VisualTransformation.None else PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password), colors = fieldColours, textStyle = mt.body,
                trailingIcon = {
                    IconButtonSmall(if (showToken) EyeOffIcon else EyeIcon, if (showToken) "Hide token" else "Show token") { showToken = !showToken }
                },
            )
            if (device.isTv) {
                Text(
                    "Typing on a TV is slow: the Google TV app on your phone can type or paste into this box.",
                    style = mt.caption, color = p.muted, modifier = Modifier.padding(top = 4.dp),
                )
            }

            heading("Notifications")
            toggle("Show notifications", draft.notify, "for new events while kitchensink isn't on screen") { draft = draft.copy(notify = it) }
            if (draft.notify && !device.canNotify) {
                button("Allow notifications", false, Modifier.padding(vertical = 4.dp), onAskNotify)
            }
            Text("Notify me about", style = mt.caption, color = p.muted, modifier = Modifier.padding(top = 4.dp))
            choice(
                listOf(Priority.Low to "Everything", Priority.Normal to "Normal and high priority", Priority.High to "High priority only"),
                draft.minPriority, enabled = draft.notify,
            ) { draft = draft.copy(minPriority = it) }
            toggle(
                "Stay connected in the background", draft.background,
                "so notifications arrive as things happen; shows a silent notification while it does",
            ) { draft = draft.copy(background = it) }
            if (draft.background && !device.unrestricted) {
                Text("Android may stop it to save battery.", style = mt.caption, color = p.warn)
                button("Let it run in the background", false, Modifier.padding(vertical = 4.dp), onUnrestrict)
            }

            heading("Display")
            Text("TV mode: stays on, full screen, for a TV or a screen on the wall", style = mt.caption, color = p.muted)
            choice(
                listOf(TvMode.Auto to if (device.isTv) "Automatic (on, as this is a TV)" else "Automatic (off, as this isn't a TV)", TvMode.On to "On", TvMode.Off to "Off"),
                draft.tv,
            ) { draft = draft.copy(tv = it) }
            Text("Theme", style = mt.caption, color = p.muted, modifier = Modifier.padding(top = 6.dp))
            choice(listOf(ThemeChoice.System to "As the system", ThemeChoice.Light to "Light", ThemeChoice.Dark to "Dark"), draft.theme) {
                draft = draft.copy(theme = it)
            }

            heading("OLED care, against burn-in")
            fun care(f: (OledCare) -> OledCare) { draft = draft.copy(oled = f(draft.oled)) }
            toggle("Shift", draft.oled.shift, "moves everything a few pixels every three minutes") { v -> care { it.copy(shift = v) } }
            toggle("True black", draft.oled.black, "a black theme with dimmer text and borders") { v -> care { it.copy(black = v) } }
            toggle("Dim when idle", draft.oled.dim, "after ten minutes without a touch or key press") { v -> care { it.copy(dim = v) } }
            toggle("Swap sides", draft.oled.swap, "moves the Recent column across every other hour") { v -> care { it.copy(swap = v) } }
            heading("")
            toggle("Cat", draft.cat, "a black cat asleep above the Recent column") { draft = draft.copy(cat = it) }

            if (panels.isNotEmpty()) {
                heading("Panel order")
                val order = orderedPanels(panels, draft).map { it.monitor }
                val titles = panels.associate { it.monitor to it.title.ifEmpty { it.monitor } }
                for ((i, id) in order.withIndex()) {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Text(titles[id] ?: id, style = mt.body, color = p.text, modifier = Modifier.weight(1f))
                        IconButtonSmall(Icons.Filled.KeyboardArrowUp, "Move up", enabled = i > 0) { draft = draft.copy(order = shift(order, id, -1)) }
                        IconButtonSmall(Icons.Filled.KeyboardArrowDown, "Move down", enabled = i < order.size - 1) { draft = draft.copy(order = shift(order, id, 1)) }
                    }
                }
                if (draft.expanded.isNotEmpty() || draft.collapsed.isNotEmpty()) {
                    button("Forget panels opened or closed by hand", false, Modifier.padding(top = 6.dp)) {
                        draft = draft.copy(expanded = emptySet(), collapsed = emptySet())
                    }
                }
            }

            Spacer(Modifier.height(20.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
                if (config.configured) button("Cancel", false, onClick = onCancel)
                button("Save", true) { onSave(draft) }
            }
        }
    }
}
