# kitchensink for Windows

A native dashboard for a kitchensink server: every monitor as a live panel,
recent events alongside, and a Windows notification for each new event.
It's written in C++ with Win32, Direct3D 11 and
[Dear ImGui](https://github.com/ocornut/imgui), and uses WinHTTP, so the
result is a single `kitchensink.exe` with nothing to install.

- Draws whatever blocks a monitor sends (stats, tables, lists, text), so new
  monitors appear without client changes.
- In a smaller window (under about 1100 px tall or 900 px wide at 100%
  scale) the layout tightens and each monitor becomes one row of its
  summary: the next events, biggest movers and so on. Full panels open
  beneath their rows, in config order, as long as they fit; the first that
  doesn't takes whatever height is left and scrolls. Click a row to open or
  close it yourself. The layout follows the window live as it's resized.
- Keeps the server's event stream open, reconnects with backoff, and
  catches up on events missed while offline. A burst of events becomes one
  notification.
- Draws its own title bar, which keeps Windows' resizing, snapping, shadow
  and double-click to maximise.
- Closing the window keeps it running in the tray; **Quit** is in the tray
  icon's menu. It can start with Windows (minimised to the tray).
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
g++ -std=c++17 -I src -I third_party tests/tests.cpp src/model.cpp src/sse.cpp && ./a.out
```

## Layout

| File | What it does |
| --- | --- |
| `src/main.cpp` | window, Direct3D, the tray icon, notifications and the message loop |
| `src/app.cpp` | the UI: header, panels, block drawing, event feed, settings |
| `src/model.cpp` | snapshots and events parsed from JSON, and the dashboard's state |
| `src/sse.cpp` | server-sent events parser |
| `src/net.cpp` | WinHTTP requests and the stream client |
| `src/images.cpp` | thumbnail download, WIC decoding and textures |
| `src/config.cpp` | settings and start-with-Windows |
| `src/theme.cpp` | palettes, fonts and ImGui style |

`third_party` holds Dear ImGui v1.91.9b and nlohmann/json v3.11.3, both MIT
licensed.
