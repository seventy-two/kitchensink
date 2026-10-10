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
};

// Icon code points in Segoe Fluent Icons / Segoe MDL2 Assets, merged into
// the body and small fonts.
namespace icon {
inline constexpr const char* Refresh = "\xEE\x9C\xAC";   // U+E72C
inline constexpr const char* Settings = "\xEE\x9C\x93";  // U+E713
inline constexpr const char* Warning = "\xEE\x9E\xBA";   // U+E7BA
}  // namespace icon

// SystemUsesDarkTheme reads the Windows "app mode" setting.
bool SystemUsesDarkTheme();

// ApplyTheme sets the palette and ImGui style for a theme and DPI scale.
void ApplyTheme(bool dark, float scale);
const Palette& Colours();

// LoadFonts rebuilds the font atlas for a DPI scale. The renderer's font
// texture must be recreated afterwards.
Fonts LoadFonts(float scale);

ImVec4 ToneColour(Tone t);

}  // namespace ks
