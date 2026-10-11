# kitchensink for Windows

A native dashboard for a kitchensink server: every monitor as a live panel,
recent events alongside, and a Windows notification for each new event.
It's written in C++ with Win32, Direct3D 11 and
[Dear ImGui](https://github.com/ocornut/imgui), and uses WinHTTP, so the
result is a single `kitchensink.exe` with nothing to install.

- Draws whatever blocks a monitor sends (stats, tables, lists, checklists,
  text) and its summary (tiles, or chips tinted by tone), so new monitors
  appear without client changes.
- In a smaller window (under about 1100 px tall or 900 px wide at 100%
  scale) the layout tightens and each monitor becomes one row of its
  summary: the next events, your holdings' moves, today's games, the hourly
  forecast and so on. Full panels open beneath their rows, in panel order,
  as long as they fit; the first that doesn't takes whatever height is left
  and scrolls, if that fits a table header and four rows. Click a row to
  open or close it yourself. Panels have no titles: an open one keeps its controls
  in a slim strip down its right edge. The layout follows the window live
  as it's resized.
- Adds two panels about the PC it runs on: **This PC** (CPU, memory, GPU
  and video memory, each with a minute's history) and **Drives** (each
  fixed drive's use). They read Windows' own counters once a second, only
  while the window is showing, and work with the server offline.
- Panels show their controls (grip, refresh, open/close) only while the
  mouse is over them. Drag a panel by the dots in its left margin to
  reorder; the order also decides which open first in the compact layout.
- **Now playing**: what's playing on this PC, from Windows' media
  sessions (the same source as the volume keys' media pop-up), so Spotify,
  browsers and other players all show. Cover, track, progress and
  previous / play-pause / next. A playing app wins over a paused one, and
  a paused track hides ten minutes after it last played.
- Checklists (the `todo` monitor) can be ticked, added to and deleted
  from; a tick shows at once, ahead of the server's update.
- Keeps the server's event stream open, reconnects with backoff, and
  catches up on events missed while offline. A burst of events becomes one
  notification.
- Draws its own title bar, which keeps Windows' resizing, snapping, shadow
  and double-click to maximise.
- Closing the window keeps it running in the tray; **Quit** is in the tray
  icon's menu. It can start with Windows (minimised to the tray).
- **OLED care** (in settings, off by default) guards OLED screens against
  burn-in: the contents move round a 6px circle every three minutes, the
  theme goes true black with dimmer text and fainter borders, everything
  dims after ten minutes without the mouse over the window (until hovered,
  or for two minutes after something new), and the Recent column swaps
  sides every other hour.
- Follows the Windows light or dark app theme (set `KITCHENSINK_THEME` to
  `light` or `dark` to override) and per-monitor DPI.
- Settings live in `%APPDATA%\kitchensink\config.json`; the token is
  encrypted with DPAPI, so only your Windows account can read it.

## Building

Needs Visual Studio 2022 (or its Build Tools) with the C++ workload.

```bat
build.bat
```

This runs the tests, then builds `build\kitchensink.exe`. The portable
parts (the data model and the server-sent events parser) also build and
test on Linux:

```sh
g++ -std=c++17 -I src -I third_party tests/tests.cpp src/model.cpp src/sse.cpp src/local.cpp && ./a.out
```

## Layout

| File | What it does |
| --- | --- |
| `src/main.cpp` | window, Direct3D, the tray icon, notifications and the message loop |
| `src/app.cpp` | the UI: header, panels, block drawing, event feed, settings |
| `src/model.cpp` | snapshots and events parsed from JSON, and the dashboard's state |
| `src/sse.cpp` | server-sent events parser |
| `src/net.cpp` | WinHTTP requests and the stream client |
| `src/media.cpp` | reads and controls Windows' media sessions (C++/WinRT) |
| `src/sysinfo.cpp` | reads CPU, memory, GPU and drive use (performance counters, DXGI) |
| `src/local.cpp` | turns those readings into the This PC and Drives panels |
| `src/images.cpp` | thumbnail download, WIC decoding and textures |
| `src/config.cpp` | settings and start-with-Windows |
| `src/theme.cpp` | palettes, fonts and ImGui style |

`third_party` holds Dear ImGui v1.91.9b and nlohmann/json v3.11.3, both MIT
licensed.
