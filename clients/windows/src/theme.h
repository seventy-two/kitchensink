// Colours, fonts and ImGui style, in light and dark variants that follow the
// Windows app theme.
#pragma once

#include "imgui.h"
#include "model.h"

namespace ks {

struct Palette {
    ImVec4 bg, card, border, text, muted, good, bad, warn, accent, hover, rowAlt, frame;
};

struct Fonts {
    ImFont* body = nullptr;
    ImFont* caption = nullptr;  // not "small": windows.h defines that as char
    ImFont* heading = nullptr;
    ImFont* big = nullptr;
    ImFont* title = nullptr;
    ImFont* chrome = nullptr;  // window button glyphs, at the size Windows uses
};

// Icon code points in Segoe Fluent Icons / Segoe MDL2 Assets, merged into
// the body and small fonts.
namespace icon {
inline constexpr const char* Refresh = "\xEE\xA2\x95";   // U+E895 Sync
inline constexpr const char* Settings = "\xEE\x9C\x80";  // U+E700 GlobalNavButton, three lines
inline constexpr const char* Warning = "\xEE\x9E\xBA";   // U+E7BA
inline constexpr const char* Minimise = "\xEE\xA4\xA1";  // U+E921
inline constexpr const char* Maximise = "\xEE\xA4\xA2";  // U+E922
inline constexpr const char* Restore = "\xEE\xA4\xA3";   // U+E923
inline constexpr const char* Close = "\xEE\xA2\xBB";     // U+E8BB
inline constexpr const char* Previous = "\xEE\xA2\x92";  // U+E892
inline constexpr const char* Next = "\xEE\xA2\x93";      // U+E893
inline constexpr const char* Play = "\xEE\x9D\xA8";      // U+E768
inline constexpr const char* Pause = "\xEE\x9D\xA9";     // U+E769
inline constexpr const char* ChevronDown = "\xEE\x9C\x8D";  // U+E70D
inline constexpr const char* ChevronUp = "\xEE\x9C\x8E";    // U+E70E
}  // namespace icon

// SystemUsesDarkTheme reads the Windows "app mode" setting.
bool SystemUsesDarkTheme();

// ApplyTheme sets the palette and ImGui style for a theme and DPI scale.
// Compact spacing is tighter, for small windows. OLED is a true-black dark
// theme with dimmer text and fainter borders, whatever Windows' theme.
void ApplyTheme(bool dark, float scale, bool compact = false, bool oled = false);
const Palette& Colours();

// LoadFonts rebuilds the font atlas for a DPI scale. The renderer's font
// texture must be recreated afterwards.
Fonts LoadFonts(float scale);

ImVec4 ToneColour(Tone t);

}  // namespace ks
