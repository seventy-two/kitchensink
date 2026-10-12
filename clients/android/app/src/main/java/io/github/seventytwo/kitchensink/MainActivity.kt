package io.github.seventytwo.kitchensink

import android.Manifest
import android.annotation.SuppressLint
import android.app.UiModeManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.os.SystemClock
import android.provider.Settings
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.ContextCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.github.seventytwo.kitchensink.data.ThemeChoice
import io.github.seventytwo.kitchensink.data.TvMode
import io.github.seventytwo.kitchensink.model.Pin
import io.github.seventytwo.kitchensink.ui.Actions
import io.github.seventytwo.kitchensink.ui.DashboardScreen
import io.github.seventytwo.kitchensink.ui.DeviceInfo
import io.github.seventytwo.kitchensink.ui.KitchensinkTheme
import io.github.seventytwo.kitchensink.ui.LocalActions
import io.github.seventytwo.kitchensink.ui.LocalPanelState
import io.github.seventytwo.kitchensink.ui.LocalTv
import io.github.seventytwo.kitchensink.ui.Metrics
import io.github.seventytwo.kitchensink.ui.Palette
import io.github.seventytwo.kitchensink.ui.PanelState
import io.github.seventytwo.kitchensink.ui.ScreenCallbacks
import io.github.seventytwo.kitchensink.ui.ScreenState
import io.github.seventytwo.kitchensink.ui.SettingsScreen
import io.github.seventytwo.kitchensink.ui.openUrl
import kotlinx.coroutines.delay

class MainActivity : ComponentActivity() {
    private val hub get() = (application as KitchensinkApp).hub

    /** When there was last a touch or key press (Unix seconds), for OLED care's dimming. */
    private val lastActivity = mutableLongStateOf(now())
    private val started = SystemClock.elapsedRealtime()

    private val isTvDevice: Boolean by lazy {
        getSystemService(UiModeManager::class.java).currentModeType == Configuration.UI_MODE_TYPE_TELEVISION ||
            packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setupFromIntent(intent)
        setContent { Root() }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setupFromIntent(intent)
    }

    /**
     * Takes the server and token from the launching intent, so a TV can be
     * set up without typing: adb shell am start -n <package>/.MainActivity
     * --es url https://… --es token …. Only before it's set up, so another
     * app can't point it somewhere else.
     */
    private fun setupFromIntent(intent: Intent?) {
        val url = intent?.getStringExtra("url") ?: return
        val token = intent.getStringExtra("token") ?: return
        if (hub.config.value.configured) return
        hub.update { it.copy(url = url.trim(), token = token.trim()) }
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        lastActivity.longValue = now()
        return super.dispatchTouchEvent(ev)
    }

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        lastActivity.longValue = now()
        return super.dispatchKeyEvent(event)
    }

    private fun applyTvMode(on: Boolean) {
        // TV mode stays on and fills the screen.
        if (on) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON) else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        val bars = WindowCompat.getInsetsController(window, window.decorView)
        if (on) {
            bars.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            bars.hide(WindowInsetsCompat.Type.systemBars())
        } else {
            bars.show(WindowInsetsCompat.Type.systemBars())
        }
    }

    private fun unrestricted(): Boolean = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)

    private fun canNotify(): Boolean =
        Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    @SuppressLint("BatteryLife")
    private fun askUnrestricted() {
        try {
            startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
        } catch (_: Exception) {
            startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
        }
    }

    @androidx.compose.runtime.Composable
    private fun Root() {
        val config by hub.config.collectAsStateWithLifecycle()
        val snapshots by hub.snapshots.collectAsStateWithLifecycle()
        val events by hub.events.collectAsStateWithLifecycle()
        val conn by hub.connState.collectAsStateWithLifecycle()
        val error by hub.connError.collectAsStateWithLifecycle()
        val refreshing by hub.refreshing.collectAsStateWithLifecycle()
        val ticked by hub.ticked.collectAsStateWithLifecycle()
        val wakeUntil by hub.wakeUntil.collectAsStateWithLifecycle()
        val drafts = remember { mutableStateMapOf<String, String>() }
        // Ago times, OLED care's moves and dimming go by the clock.
        val time by produceState(now()) {
            while (true) {
                value = now()
                delay(10_000)
            }
        }
        var settingsOpen by rememberSaveable { mutableStateOf(false) }
        var device by remember { mutableStateOf(DeviceInfo(isTvDevice, unrestricted(), canNotify())) }
        val askNotify = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {
            device = device.copy(canNotify = canNotify())
        }
        val context = LocalContext.current

        val tv = when (config.tv) {
            TvMode.Auto -> isTvDevice
            TvMode.On -> true
            TvMode.Off -> false
        }
        LaunchedEffect(tv) { applyTvMode(tv) }
        LaunchedEffect(Unit) {
            if (!config.configured) settingsOpen = true
            else if (config.notify && !canNotify() && Build.VERSION.SDK_INT >= 33) askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        LaunchedEffect(settingsOpen) {
            if (settingsOpen) device = device.copy(unrestricted = unrestricted(), canNotify = canNotify())
        }

        val dark = when (config.theme) {
            ThemeChoice.System -> isSystemInDarkTheme()
            ThemeChoice.Light -> false
            ThemeChoice.Dark -> true
        }
        val palette = if (config.oled.black) Palette.Oled else if (dark) Palette.Dark else Palette.Light
        WindowCompat.getInsetsController(window, window.decorView).isAppearanceLightStatusBars = !palette.dark

        val actions = remember {
            object : Actions {
                override fun open(url: String) = openUrl(context, url)
                override fun refresh(monitor: String) = hub.refresh(monitor)
                override fun toggle(monitor: String, id: String, done: Boolean) = hub.toggle(monitor, id, done)
                override fun delete(monitor: String, id: String) = hub.delete(monitor, id)
                override fun add(monitor: String, text: String) = hub.add(monitor, text)
            }
        }
        val callbacks = remember {
            object : ScreenCallbacks {
                override fun pin(monitor: String, pin: Pin) = hub.update {
                    it.copy(
                        expanded = if (pin == Pin.Open) it.expanded + monitor else it.expanded - monitor,
                        collapsed = if (pin == Pin.Closed) it.collapsed + monitor else it.collapsed - monitor,
                    )
                }
                override fun clear(id: Long) = hub.clearEvent(id)
                override fun clearAll() = hub.clearAll()
                override fun togglePrivate() = hub.update { it.copy(hidePrivate = !it.hidePrivate) }
                override fun openSettings() {
                    settingsOpen = true
                }
                override fun titleOf(monitor: String) = hub.titleOf(monitor)
            }
        }

        KitchensinkTheme(palette, Metrics.of(compact = true)) {
            CompositionLocalProvider(
                LocalActions provides actions,
                LocalTv provides tv,
                LocalPanelState provides PanelState(ticked, refreshing.keys, time, drafts),
            ) {
                val inset = if (tv) Modifier else Modifier.safeDrawingPadding()
                Box(Modifier.fillMaxSize().then(if (settingsOpen) Modifier else inset)) {
                    if (settingsOpen) {
                        BackHandler(config.configured) { settingsOpen = false }
                        Box(Modifier.safeDrawingPadding()) {
                            SettingsScreen(
                                config, snapshots, device,
                                onSave = {
                                    hub.update { _ -> it }
                                    settingsOpen = false
                                    if (it.notify && !canNotify() && Build.VERSION.SDK_INT >= 33) askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
                                },
                                onCancel = { settingsOpen = false },
                                onUnrestrict = ::askUnrestricted,
                                onAskNotify = { if (Build.VERSION.SDK_INT >= 33) askNotify.launch(Manifest.permission.POST_NOTIFICATIONS) },
                            )
                        }
                    } else {
                        DashboardScreen(
                            ScreenState(
                                config, snapshots, events, conn, error, time, lastActivity.longValue, wakeUntil,
                                tv = tv, overscan = tv && isTvDevice,
                                clock = { (SystemClock.elapsedRealtime() - started) / 1000.0 },
                            ),
                            callbacks,
                        )
                    }
                }
            }
        }
    }

    private companion object {
        fun now() = System.currentTimeMillis() / 1000
    }
}
