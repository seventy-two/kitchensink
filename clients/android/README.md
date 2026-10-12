# kitchensink for Android

A native dashboard for a kitchensink server on phones, tablets and Android
TV: every monitor as a live panel, recent events alongside, and a
notification for each new event. It's Kotlin and Jetpack Compose, one APK
for all three.

- Draws whatever blocks a monitor sends (stats, tables, lists, checklists,
  text) and its summary (tiles, or chips tinted by tone, with the
  forecast's weather icons and chance-of-rain chart), so new monitors
  appear without client changes. The drawing follows the Windows client's.
- **Compact layout** (phones, tablets on their side, TVs): each monitor is
  one row of its summary, and full panels open beneath their rows, in panel
  order, as long as they fit the screen; the first that doesn't takes what's
  left and scrolls, if that fits a table header and four rows. Tap a row
  to open or close it yourself. Tall, wide screens (from about 840 x 900 dp)
  show every full panel in columns instead.
- **Portrait and landscape**: from 720 dp wide, Recent is a column beside
  the panels; narrower, the panels and Recent are two pages, swiped between,
  with a dot on Recent's tab for anything new.
- **TV mode** (on by itself on Android TV, or on in settings for a tablet
  on the wall): stays on, full screen, with room for overscan on a TV. A
  remote's D-pad moves between rows, buttons and events, outlined in the
  accent colour, and select opens or closes a panel.
- **Notifications** for new events while the dashboard isn't on screen,
  filtered by priority, a burst becoming one. High-priority events use
  their own channel, which can sound and pop up. To get them as things
  happen, the app can stay connected in the background (a foreground
  service with a silent, minimised notification); it restarts after the
  phone does. Settings has a button to stop Android putting it to sleep to
  save battery.
- Checklists (the `todo` monitor) can be ticked, added to and deleted from;
  a tick shows at once, ahead of the server's update.
- Keeps the event stream open, reconnects with backoff, and catches up on
  events missed while offline.
- The eye in the header, there when a monitor sends private figures, hides
  or shows them.
- Swipe a Recent card away, or tap its ×, to clear it; the × by the heading
  clears them all. Cleared events stay cleared after a restart.
- **OLED care** and the **cat**, as on Windows: shift, true black, dim when
  idle (after ten minutes without a touch or key press), swap sides, and a
  black cat asleep above Recent.
- Panel order is set in settings, with up and down buttons.
- Light, dark or the system's theme. Settings live in the app's private
  storage; the token is encrypted with a key in the Android Keystore.

## Setting up

Enter the server's address (e.g. `https://example.com/kitchensink`) and
token in settings. On a TV, the Google TV app on a phone can type or paste
into the box, or with adb:

```sh
adb shell am start -n io.github.seventytwo.kitchensink/.MainActivity --es url https://example.com/kitchensink --es token …
```

That only works before it's set up, so another app can't repoint it.

## Building

Needs JDK 21 and the Android SDK (platform 37, build tools 37), with
`sdk.dir` in `local.properties` or `ANDROID_HOME` set.

```sh
./gradlew testDebugUnitTest assembleRelease
```

The release APK is in `app/build/outputs/apk/release/`. It's signed with
the key named by the `ks.keystore`, `ks.storePassword`, `ks.keyAlias` and
`ks.keyPassword` Gradle properties (put them in `~/.gradle/gradle.properties`,
not here), or the debug key without them. Keep using the same key, or
Android won't install an update over the last.

The tests cover the model, layout decisions, chips, the SSE parser and the
stream client (against a mock server). Screenshot tests draw the dashboard
from a made-up sample with Robolectric at phone, tablet and TV sizes; to
look at them:

```sh
./gradlew testDebugUnitTest -Proborazzi.test.record=true   # then see app/build/screenshots/
```

## Layout

| File | What it does |
| --- | --- |
| `model/Model.kt` | snapshots and events parsed from JSON, private figures |
| `model/Dashboard.kt` | the dashboard's state built from the stream, clearing events |
| `model/Layout.kt` | panel order, which panels open in the compact layout, OLED care |
| `model/Chips.kt` | how summary chips share a row |
| `net/Sse.kt`, `net/StreamClient.kt` | server-sent events, and the client that keeps the stream open |
| `Hub.kt` | the app's state: settings, the stream, refreshes, ticks, notifications |
| `data/Config.kt` | settings and the encrypted token |
| `notify/Notifier.kt`, `service/LiveService.kt` | notifications, and staying connected in the background |
| `ui/` | the screens: panels, blocks, summaries, Recent, settings |
| `draw/` | the weather icons and the cat, drawn from shapes |
