#include "theme.h"

#include <windows.h>

#include <string>

namespace ks {

namespace {

ImVec4 hex(unsigned rgb, float a = 1.0f) {
    return ImVec4(((rgb >> 16) & 0xFF) / 255.0f, ((rgb >> 8) & 0xFF) / 255.0f, (rgb & 0xFF) / 255.0f, a);
}

const Palette kDark = {
    hex(0x15171C), hex(0x1E2128), hex(0x2B2F38), hex(0xE6E8EC), hex(0x8C929D), hex(0x3FB950),
    hex(0xF8645A), hex(0xD9A23A), hex(0x5AA8FF), hex(0x262A33), hex(0x22252D), hex(0x2A2E37),
};

const Palette kLight = {
    hex(0xF2F3F5), hex(0xFFFFFF), hex(0xE1E4E8), hex(0x1F2328), hex(0x656D76), hex(0x1A7F37),
    hex(0xCF222E), hex(0x9A6700), hex(0x0969DA), hex(0xF0F3F6), hex(0xF7F8FA), hex(0xEEF0F3),
};

Palette current = kDark;

std::string fontPath(const char* file) {
    char dir[MAX_PATH];
    GetWindowsDirectoryA(dir, MAX_PATH);
    return std::string(dir) + "\\Fonts\\" + file;
}

bool exists(const std::string& path) { return GetFileAttributesA(path.c_str()) != INVALID_FILE_ATTRIBUTES; }

}  // namespace

bool SystemUsesDarkTheme() {
    // KITCHENSINK_THEME=light or dark overrides the Windows setting.
    wchar_t forced[16];
    if (GetEnvironmentVariableW(L"KITCHENSINK_THEME", forced, ARRAYSIZE(forced))) return wcscmp(forced, L"light") != 0;
    DWORD light = 1, size = sizeof(light);
    RegGetValueW(HKEY_CURRENT_USER, L"Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize",
                 L"AppsUseLightTheme", RRF_RT_REG_DWORD, nullptr, &light, &size);
    return light == 0;
}

const Palette& Colours() { return current; }

ImVec4 ToneColour(Tone t) {
    switch (t) {
    case Tone::Good: return current.good;
    case Tone::Bad: return current.bad;
    case Tone::Warn: return current.warn;
    case Tone::Muted: return current.muted;
    case Tone::Accent: return current.accent;
    default: return current.text;
    }
}

void ApplyTheme(bool dark, float scale) {
    current = dark ? kDark : kLight;
    const Palette& p = current;

    ImGuiStyle& s = ImGui::GetStyle();
    s = ImGuiStyle();
    s.WindowPadding = ImVec2(20, 16);
    s.FramePadding = ImVec2(10, 6);
    s.ItemSpacing = ImVec2(8, 6);
    s.ItemInnerSpacing = ImVec2(6, 4);
    s.CellPadding = ImVec2(8, 5);
    s.ScrollbarSize = 10;
    s.WindowRounding = 0;
    s.ChildRounding = 10;
    s.FrameRounding = 6;
    s.PopupRounding = 8;
    s.ScrollbarRounding = 8;
    s.GrabRounding = 6;
    s.WindowBorderSize = 0;
    s.ChildBorderSize = 1;
    s.PopupBorderSize = 1;
    s.FrameBorderSize = 0;
    s.ScaleAllSizes(scale);

    ImVec4* c = s.Colors;
    ImVec4 none(0, 0, 0, 0);
    c[ImGuiCol_Text] = p.text;
    c[ImGuiCol_TextDisabled] = p.muted;
    c[ImGuiCol_WindowBg] = p.bg;
    c[ImGuiCol_ChildBg] = none;
    c[ImGuiCol_PopupBg] = p.card;
    c[ImGuiCol_Border] = p.border;
    c[ImGuiCol_BorderShadow] = none;
    c[ImGuiCol_FrameBg] = p.frame;
    c[ImGuiCol_FrameBgHovered] = p.hover;
    c[ImGuiCol_FrameBgActive] = p.hover;
    c[ImGuiCol_TitleBg] = p.card;
    c[ImGuiCol_TitleBgActive] = p.card;
    c[ImGuiCol_ScrollbarBg] = none;
    c[ImGuiCol_ScrollbarGrab] = p.border;
    c[ImGuiCol_ScrollbarGrabHovered] = p.muted;
    c[ImGuiCol_ScrollbarGrabActive] = p.muted;
    c[ImGuiCol_CheckMark] = p.accent;
    c[ImGuiCol_Button] = p.frame;
    c[ImGuiCol_ButtonHovered] = p.hover;
    c[ImGuiCol_ButtonActive] = p.border;
    c[ImGuiCol_Header] = p.hover;
    c[ImGuiCol_HeaderHovered] = p.hover;
    c[ImGuiCol_HeaderActive] = p.border;
    c[ImGuiCol_Separator] = p.border;
    c[ImGuiCol_TableHeaderBg] = none;
    c[ImGuiCol_TableBorderLight] = p.border;
    c[ImGuiCol_TableBorderStrong] = p.border;
    c[ImGuiCol_TableRowBg] = none;
    c[ImGuiCol_TableRowBgAlt] = p.rowAlt;
    c[ImGuiCol_TextSelectedBg] = ImVec4(p.accent.x, p.accent.y, p.accent.z, 0.35f);
    c[ImGuiCol_ModalWindowDimBg] = ImVec4(0, 0, 0, dark ? 0.55f : 0.3f);
    c[ImGuiCol_NavHighlight] = p.accent;
}

Fonts LoadFonts(float scale) {
    ImGuiIO& io = ImGui::GetIO();
    io.Fonts->Clear();

    // Latin, general punctuation (– · …), currency, arrows and shapes.
    static const ImWchar text[] = {0x0020, 0x017F, 0x2000, 0x206F, 0x20A0, 0x20CF, 0x2190,
                                   0x21FF, 0x2212, 0x2212, 0x25A0, 0x25FF, 0};
    static const ImWchar icons[] = {0xE700, 0xF8FF, 0};

    std::string regular = fontPath("segoeui.ttf"), semibold = fontPath("seguisb.ttf");
    std::string iconFont = fontPath("SegoeIcons.ttf");
    if (!exists(iconFont)) iconFont = fontPath("segmdl2.ttf");
    if (!exists(semibold)) semibold = regular;

    auto add = [&](const std::string& path, float px, bool withIcons) -> ImFont* {
        ImFontConfig cfg;
        cfg.OversampleH = 2;
        ImFont* f = exists(path) ? io.Fonts->AddFontFromFileTTF(path.c_str(), px * scale, &cfg, text) : nullptr;
        if (!f) {
            ImFontConfig def;
            def.SizePixels = px * scale;
            return io.Fonts->AddFontDefault(&def);
        }
        if (withIcons && exists(iconFont)) {
            ImFontConfig merge;
            merge.MergeMode = true;
            merge.GlyphOffset = ImVec2(0, 2 * scale);
            io.Fonts->AddFontFromFileTTF(iconFont.c_str(), px * scale * 0.95f, &merge, icons);
        }
        return f;
    };

    Fonts f;
    f.body = add(regular, 16, true);
    f.caption = add(regular, 13.5f, true);
    f.heading = add(semibold, 16, false);
    f.big = add(semibold, 26, false);
    f.title = add(semibold, 20, false);
    if (exists(iconFont)) {
        ImFontConfig cfg;
        cfg.OversampleH = 2;
        f.chrome = io.Fonts->AddFontFromFileTTF(iconFont.c_str(), 10 * scale, &cfg, icons);
    }
    if (!f.chrome) f.chrome = f.caption;
    io.FontDefault = f.body;
    return f;
}

}  // namespace ks
