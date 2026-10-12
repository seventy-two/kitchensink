#include "app.h"

#include <windows.h>
#include <shellapi.h>

#include <algorithm>
#include <cfloat>
#include <chrono>
#include <cmath>
#include <ctime>

#include "imgui.h"
#include "imgui_internal.h"
#include "cat.h"
#include "local.h"

namespace ks {

namespace {

ImU32 u32(const ImVec4& c) { return ImGui::ColorConvertFloat4ToU32(c); }

ImVec4 fade(ImVec4 c, float a) {
    c.w = a;
    return c;
}

int64_t now() { return int64_t(std::time(nullptr)); }

const Priority kPriorities[] = {Priority::Low, Priority::Normal, Priority::High};
const char* kPriorityNames[] = {"Everything", "Normal and high priority", "High priority only"};

// textAligned draws text in the current table cell or line, aligned within
// the space left on it.
void textAligned(const std::string& s, Align align, const ImVec4& colour) {
    float w = ImGui::CalcTextSize(s.c_str()).x;
    float avail = ImGui::GetContentRegionAvail().x;
    if (align == Align::Right && avail > w) ImGui::SetCursorPosX(ImGui::GetCursorPosX() + avail - w);
    if (align == Align::Center && avail > w) ImGui::SetCursorPosX(ImGui::GetCursorPosX() + (avail - w) / 2);
    ImGui::TextColored(colour, "%s", s.c_str());
}

// linkBehaviour makes the last item open url when clicked.
void linkBehaviour(const std::string& url) {
    if (url.empty() || !ImGui::IsItemHovered()) return;
    ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
    ImVec2 min = ImGui::GetItemRectMin(), max = ImGui::GetItemRectMax();
    ImGui::GetWindowDrawList()->AddLine(ImVec2(min.x, max.y), max, u32(Colours().muted));
    if (ImGui::IsItemClicked()) OpenURL(url);
}

void mutedText(const std::string& s) { ImGui::TextColored(Colours().muted, "%s", s.c_str()); }

// fit shortens s with an ellipsis to fit width in the current font.
std::string fit(const std::string& s, float width) {
    if (ImGui::CalcTextSize(s.c_str()).x <= width) return s;
    std::string out = s;
    while (!out.empty()) {
        // Drop one UTF-8 character: its continuation bytes, then its lead.
        while (!out.empty() && (static_cast<unsigned char>(out.back()) & 0xC0) == 0x80) out.pop_back();
        if (!out.empty()) out.pop_back();
        while (!out.empty() && out.back() == ' ') out.pop_back();
        if (ImGui::CalcTextSize((out + "…").c_str()).x <= width) return out + "…";
    }
    return "…";
}

// fitLines shortens each line of s to fit width, as fit does.
std::string fitLines(const std::string& s, float width) {
    std::string out;
    size_t start = 0;
    for (;;) {
        size_t end = s.find('\n', start);
        out += fit(s.substr(start, end == std::string::npos ? std::string::npos : end - start), width);
        if (end == std::string::npos) return out;
        out += '\n';
        start = end + 1;
    }
}

void wrapped(const std::string& s, const ImVec4& colour) {
    ImGui::PushStyleColor(ImGuiCol_Text, colour);
    ImGui::PushTextWrapPos(0);
    ImGui::TextUnformatted(s.c_str());
    ImGui::PopTextWrapPos();
    ImGui::PopStyleColor();
}

// sparkline draws a series as a filled line chart, from 0 (or below) to
// top, or to the series' highest point when top is 0.
void sparkline(const std::vector<float>& v, ImVec2 size, const ImVec4& colour, float top) {
    ImVec2 p = ImGui::GetCursorScreenPos();
    ImGui::Dummy(size);
    if (v.size() < 2) return;
    auto [lo, hi] = std::minmax_element(v.begin(), v.end());
    float min = std::min(*lo, 0.0f), range = std::max((top > 0 ? std::max(top, *hi) : *hi) - min, 1e-6f);
    ImDrawList* dl = ImGui::GetWindowDrawList();
    std::vector<ImVec2> pts;
    for (size_t i = 0; i < v.size(); i++)
        pts.push_back(ImVec2(p.x + size.x * float(i) / float(v.size() - 1), p.y + size.y - size.y * (v[i] - min) / range));
    float base = p.y + size.y;
    for (size_t i = 0; i + 1 < pts.size(); i++) {
        ImVec2 quad[4] = {pts[i], pts[i + 1], ImVec2(pts[i + 1].x, base), ImVec2(pts[i].x, base)};
        dl->AddConvexPolyFilled(quad, 4, u32(fade(colour, 0.18f)));
    }
    dl->AddPolyline(pts.data(), int(pts.size()), u32(colour), 0, 1.5f * ImGui::GetStyle().FramePadding.y / 6);
}

}  // namespace

void OpenURL(const std::string& url) {
    if (url.rfind("https://", 0) != 0 && url.rfind("http://", 0) != 0) return;
    ShellExecuteW(nullptr, L"open", Widen(url).c_str(), nullptr, nullptr, SW_SHOWNORMAL);
}

App::App(HWND hwnd, ID3D11Device* device, std::function<void()> wake,
         std::function<void(const std::string&, const std::string&)> notify)
    : hwnd_(hwnd),
      config_(LoadConfig()),
      client_(std::make_unique<Client>(wake)),
      images_(std::make_unique<ImageCache>(device, wake)),
      local_(std::make_unique<LocalStats>(wake)),
      media_(std::make_unique<MediaWatcher>(wake, images_.get())),
      notify_(std::move(notify)) {
    // Idle dimming counts from launch, not from 1970.
    lastActivity_ = now();
    model_.SetCleared(config_.cleared);
    client_->Start(config_.url, config_.token);
    if (config_.url.empty() || config_.token.empty()) openSettings();
}

App::~App() {
    media_.reset();  // before images_, which it feeds
    local_.reset();
    client_.reset();
    images_.reset();
}

void App::SetScale(float scale) {
    scale_ = scale;
    ApplyTheme(dark_, scale_, compact_, config_.oled.black);
    fonts_ = LoadFonts(scale_);
}

void App::SetDark(bool dark) {
    dark_ = dark;
    ApplyTheme(dark_, scale_, compact_, config_.oled.black);
}

void App::Pump() {
    std::vector<Inbound> in;
    client_->Drain(in);
    for (auto& m : in) {
        switch (m.type) {
        case Inbound::Type::SyncStart: model_.BeginSync(); break;
        case Inbound::Type::Ready: model_.EndSync(); break;
        case Inbound::Type::Snapshot: {
            refreshing_.erase(m.snapshot.monitor);
            std::string prefix = m.snapshot.monitor + "/";
            for (auto it = ticked_.begin(); it != ticked_.end();)
                it = it->first.rfind(prefix, 0) == 0 ? ticked_.erase(it) : std::next(it);
            model_.ApplySnapshot(std::move(m.snapshot));
            break;
        }
        case Inbound::Type::Event: {
            Event e = m.event;
            if (!model_.AddEvent(std::move(m.event))) break;
            // History from before the app started has already been seen
            // (or missed for good); events missed while offline are new.
            if (!m.history && !e.quiet) wakeUntil_ = now() + 2 * 60;  // OLED care: show it
            if (!m.history && !e.quiet && config_.notify && e.priority >= config_.minPriority)
                pending_.push_back(std::move(e));
            break;
        }
        }
    }
    images_->Upload();
    local_->Take(localPanels_);
    media_->Take(mediaPanel_);

    if (pending_.empty()) return;
    if (pendingSince_ == 0) pendingSince_ = GetTickCount64();
    if (FlushIn() > 0) return;

    if (pending_.size() == 1) {
        const Event& e = pending_[0];
        lastNotifyURL_ = e.url;
        notify_(e.title, e.body.empty() ? model_.TitleOf(e.monitor) : e.body);
    } else {
        // Several at once, e.g. after being offline: one summary, newest
        // first.
        lastNotifyURL_.clear();
        std::string body;
        for (size_t i = pending_.size(); i-- > 0;) {
            if (!body.empty()) body += "\n";
            body += pending_[i].title;
        }
        notify_(std::to_string(pending_.size()) + " new events", body);
    }
    pending_.clear();
    pendingSince_ = 0;
}

unsigned long App::FlushIn() const {
    constexpr unsigned long long kHoldMs = 1500;
    if (pendingSince_ == 0) return INFINITE;
    unsigned long long waited = GetTickCount64() - pendingSince_;
    return waited >= kHoldMs ? 0 : (unsigned long)(kHoldMs - waited);
}

bool App::NotificationClicked() {
    if (lastNotifyURL_.empty()) return false;
    OpenURL(lastNotifyURL_);
    return true;
}

void App::Frame() {
    catInterval_ = 0;  // until she's drawn
    ImGuiIO& io = ImGui::GetIO();
    if (bool c = UseCompact(compact_, io.DisplaySize.x, io.DisplaySize.y, scale_); c != compact_) {
        compact_ = c;
        ApplyTheme(dark_, scale_, compact_, config_.oled.black);
    }
    ImGui::SetNextWindowPos(ImVec2(0, 0));
    ImGui::SetNextWindowSize(io.DisplaySize);
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(0, 0));
    ImGui::Begin("##root", nullptr,
                 ImGuiWindowFlags_NoDecoration | ImGuiWindowFlags_NoMove | ImGuiWindowFlags_NoSavedSettings |
                     ImGuiWindowFlags_NoBringToFrontOnFocus | ImGuiWindowFlags_NoScrollbar |
                     ImGuiWindowFlags_NoScrollWithMouse);
    ImGui::PopStyleVar();

    // OLED care moves everything round a small circle, inside a margin left
    // for it. The window itself stays full size, so nothing behind it (the
    // panels being measured) shows round the edge.
    int64_t t = now();
    float margin = config_.oled.shift ? std::round(6 * scale_) : 0;
    auto [dx, dy] = config_.oled.shift ? OledShift(t, margin) : std::pair<float, float>{0.0f, 0.0f};
    ImGui::SetCursorPos(ImVec2(margin + dx, margin + dy));
    ImGui::BeginChild("##contents", ImVec2(io.DisplaySize.x - margin * 2, io.DisplaySize.y - margin * 2),
                      ImGuiChildFlags_AlwaysUseWindowPadding,
                      ImGuiWindowFlags_NoBackground | ImGuiWindowFlags_NoScrollbar | ImGuiWindowFlags_NoScrollWithMouse);

    header();
    local_->Want();
    media_->Want();

    float gap = (compact_ ? 8 : 16) * scale_;
    float avail = ImGui::GetContentRegionAvail().x;
    float feedW = avail >= 980 * scale_ ? (compact_ ? 300 : 340) * scale_ : 0;
    float cardsW = feedW > 0 ? avail - feedW - gap : avail;

    ConnState state = client_->State();
    if (state == ConnState::NotConfigured) {
        empty("Add your server's address and token to get started.", true);
    } else if (model_.Snapshots().empty() && localPanels_.empty()) {
        empty(state == ConnState::Offline ? "Can't reach the server." : "Connecting…", state == ConnState::Offline);
    } else {
        // OLED care swaps the side column's side every other hour.
        bool feedFirst = feedW > 0 && config_.oled.swap && OledSwapSides(t);
        auto drawFeed = [&] {
            ImGui::BeginGroup();
            if (config_.cat) cat(feedW);
            feedHeading(feedW);
            // Its scrollbar only shows on hover, over the content, so the
            // cards reach the same distance from both edges.
            ImGui::BeginChild("##feed", ImVec2(feedW, 0), ImGuiChildFlags_None,
                              ImGuiWindowFlags_NoBackground | ImGuiWindowFlags_NoScrollbar);
            feed();
            overlayScrollbar();
            ImGui::EndChild();
            ImGui::EndGroup();
            feedRect_ = ImRect(ImGui::GetItemRectMin(), ImGui::GetItemRectMax());
        };
        if (feedFirst) {
            drawFeed();
            ImGui::SameLine(0, gap);
        }
        ImGui::BeginChild("##cards", ImVec2(cardsW, 0), ImGuiChildFlags_None, ImGuiWindowFlags_NoBackground);
        cards(ImGui::GetContentRegionAvail().x);
        ImGui::EndChild();
        if (feedW > 0 && !feedFirst) {
            ImGui::SameLine(0, gap);
            drawFeed();
        }
    }

    ImGui::EndChild();
    settings();

    // OLED care dims everything when the mouse hasn't been over the window
    // for a while; it's back as soon as it is.
    if (ImGui::IsMousePosValid() || settingsOpen_) lastActivity_ = t;
    bool dimmed = config_.oled.dim && OledDim(t, lastActivity_, wakeUntil_);
    if (dimmed) ImGui::GetForegroundDrawList()->AddRectFilled(ImVec2(0, 0), io.DisplaySize, IM_COL32(0, 0, 0, 153));
    // The cat isn't drawn while the window's dimmed.
    frameInterval_ = dimmed ? 0 : catInterval_;
    busy_ = ImGui::IsAnyItemActive() || ImGui::GetIO().WantTextInput || settingsOpen_;
    ImGui::End();
}

std::vector<Snapshot> App::panels() const {
    std::vector<Snapshot> out;
    if (!localPanels_.empty()) out.push_back(localPanels_.front());
    if (!mediaPanel_.blocks.empty()) out.push_back(mediaPanel_);
    for (const Snapshot& s : model_.Snapshots()) out.push_back(config_.hidePrivate ? WithoutPrivate(s) : s);
    if (localPanels_.size() > 1) out.push_back(localPanels_.back());
    if (config_.order.empty()) return out;
    std::vector<std::string> ids;
    for (const auto& p : out) ids.push_back(p.monitor);
    std::vector<Snapshot> arranged;
    for (const auto& id : Arrange(ids, config_.order))
        arranged.push_back(*std::find_if(out.begin(), out.end(), [&](const Snapshot& p) { return p.monitor == id; }));
    return arranged;
}

// hoveringCard reports whether the mouse is over the card being drawn,
// including the panels inside it, even while one of its items is held.
bool App::hoveringCard() const {
    ImVec2 min = ImGui::GetWindowPos();
    ImVec2 max(min.x + ImGui::GetWindowWidth(), min.y + ImGui::GetWindowHeight());
    return ImGui::IsWindowHovered(ImGuiHoveredFlags_ChildWindows | ImGuiHoveredFlags_AllowWhenBlockedByActiveItem) &&
           ImGui::IsMouseHoveringRect(min, max);
}

bool App::InCaption(float x, float y) const {
    if (y >= captionTop_ + captionH_) return false;
    for (const ImRect& r : noDrag_)
        if (r.Contains(ImVec2(x, y))) return false;
    return true;
}

// header draws the title bar: the name and connection status on the left,
// settings and the window buttons on the right. Everything else on it
// drags the window (see InCaption).
void App::header() {
    const Palette& p = Colours();
    ImVec2 origin = ImGui::GetWindowPos();
    float width = ImGui::GetWindowWidth();
    captionH_ = (compact_ ? 36 : 44) * scale_;
    captionTop_ = origin.y;
    noDrag_.clear();

    // Windows 10 draws no border along the top of a window without a
    // standard title bar, so draw one to match the sides.
    // It belongs at the window's real edge, wherever OLED care has moved
    // the contents.
    if (!IsZoomed(hwnd_))
        ImGui::GetBackgroundDrawList()->AddLine(ImVec2(0, 0.5f), ImVec2(ImGui::GetIO().DisplaySize.x, 0.5f), u32(p.border));

    float left = windowButtons();

    ImGui::PushFont(fonts_.title);
    float titleH = ImGui::GetTextLineHeight();
    float mid = captionH_ / 2;
    ImGui::SetCursorPos(ImVec2(ImGui::GetStyle().WindowPadding.x, mid - titleH / 2));
    ImGui::TextUnformatted("kitchensink");
    ImGui::PopFont();

    ConnState state = client_->State();
    ImVec4 dot = p.muted;
    std::string status;
    switch (state) {
    case ConnState::NotConfigured: status = "Not set up"; break;
    case ConnState::Connecting: status = "Connecting…", dot = p.warn; break;
    case ConnState::Connected: status = "Live", dot = p.good; break;
    case ConnState::Offline: {
        std::string err = client_->LastError();
        status = err.empty() ? "Offline" : "Offline: " + err;
        dot = p.bad;
        break;
    }
    }
    // The dot and status sit centred on the title's line.
    ImGui::SameLine(0, 16 * scale_);
    ImGui::PushFont(fonts_.caption);
    float textH = ImGui::GetTextLineHeight();
    ImGui::SetCursorPosY(mid - textH / 2);
    ImVec2 pos = ImGui::GetCursorScreenPos();
    float r = 4 * scale_;
    ImGui::GetWindowDrawList()->AddCircleFilled(ImVec2(pos.x + r, pos.y + textH / 2), r, u32(dot));
    ImGui::Dummy(ImVec2(r * 2, textH));
    ImGui::SameLine(0, 6 * scale_);
    ImGui::SetCursorPosY(mid - textH / 2);
    // Long errors stop short of the buttons.
    ImGui::PushClipRect(ImGui::GetCursorScreenPos(), ImVec2(left - 48 * scale_, origin.y + captionH_), true);
    mutedText(status);
    ImGui::PopClipRect();
    ImGui::PopFont();

    float bw = ImGui::CalcTextSize(icon::Settings).x + ImGui::GetStyle().FramePadding.x * 2;
    ImGui::SameLine();
    ImGui::SetCursorPos(ImVec2(left - origin.x - 8 * scale_ - bw, mid - ImGui::GetFrameHeight() / 2));
    // Flat until hovered, like the window buttons beside it.
    ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
    bool open = ImGui::Button(icon::Settings);
    ImGui::PopStyleColor();
    if (open) openSettings();
    noDrag_.push_back(ImRect(ImGui::GetItemRectMin(), ImGui::GetItemRectMax()));
    if (ImGui::IsItemHovered()) ImGui::SetTooltip("Settings");

    // Hide or show private figures, e.g. a portfolio, at a click: only
    // there when a monitor sends some.
    const auto& snaps = model_.Snapshots();
    if (std::any_of(snaps.begin(), snaps.end(), [](const Snapshot& s) { return HasPrivate(s); })) {
        float ew = ImGui::CalcTextSize(icon::View).x + ImGui::GetStyle().FramePadding.x * 2;
        ImGui::SameLine();
        ImGui::SetCursorPos(ImVec2(left - origin.x - 8 * scale_ - bw - 4 * scale_ - ew, mid - ImGui::GetFrameHeight() / 2));
        ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
        ImGui::PushStyleColor(ImGuiCol_Text, config_.hidePrivate ? p.muted : p.text);
        bool toggle = ImGui::Button(icon::View);
        ImGui::PopStyleColor(2);
        // Struck through while hidden: Windows 10's icon font has no such
        // eye.
        if (config_.hidePrivate) {
            ImVec2 a = ImGui::GetItemRectMin(), b = ImGui::GetItemRectMax(), c((a.x + b.x) / 2, (a.y + b.y) / 2);
            float r = 7 * scale_;
            ImGui::GetWindowDrawList()->AddLine(ImVec2(c.x - r, c.y - r + 2 * scale_), ImVec2(c.x + r, c.y + r + 2 * scale_),
                                                u32(p.muted), 1.5f * scale_);
        }
        noDrag_.push_back(ImRect(ImGui::GetItemRectMin(), ImGui::GetItemRectMax()));
        if (ImGui::IsItemHovered())
            ImGui::SetTooltip("%s", config_.hidePrivate ? "Show private figures" : "Hide private figures");
        if (toggle) {
            config_.hidePrivate = !config_.hidePrivate;
            SaveConfig(config_);
        }
    }
    ImGui::SetCursorPosY(captionH_ + (compact_ ? 2 : 8) * scale_);
}

// windowButtons draws minimise, maximise and close in the top-right corner,
// like Windows' own, and returns where they start. They read the mouse
// directly rather than being ImGui items, so they keep working while the
// settings dialog is open.
float App::windowButtons() {
    const Palette& p = Colours();
    const ImGuiIO& io = ImGui::GetIO();
    ImDrawList* dl = ImGui::GetWindowDrawList();
    ImVec2 origin = ImGui::GetWindowPos();
    float w = std::round(46 * scale_), h = captionH_;
    float x = origin.x + ImGui::GetWindowWidth();
    bool active = GetForegroundWindow() == hwnd_;
    bool zoomed = IsZoomed(hwnd_);

    ImGui::PushClipRect(origin, ImVec2(x, origin.y + h), false);
    ImGui::PushFont(fonts_.chrome);
    enum { Minimise, Maximise, Close };
    const char* glyphs[] = {icon::Minimise, zoomed ? icon::Restore : icon::Maximise, icon::Close};
    const char* tips[] = {"Minimise", zoomed ? "Restore down" : "Maximise", "Close (keeps running in the tray)"};
    x -= 3 * w;
    float left = x;
    bool anyHovered = false;
    for (int i = 0; i < 3; i++, x += w) {
        ImRect r(ImVec2(x, origin.y), ImVec2(x + w, origin.y + h));
        noDrag_.push_back(r);
        bool hovered = r.Contains(io.MousePos);
        bool held = hovered && io.MouseDown[0] && r.Contains(io.MouseClickedPos[0]);
        bool clicked = hovered && io.MouseReleased[0] && r.Contains(io.MouseClickedPos[0]);

        ImVec4 glyph = active ? p.text : p.muted;
        if (i == Close && (hovered || held)) {
            dl->AddRectFilled(r.Min, r.Max, held ? IM_COL32(0xC4, 0x2B, 0x1C, 0xE6) : IM_COL32(0xC4, 0x2B, 0x1C, 0xFF));
            glyph = ImVec4(1, 1, 1, 1);
        } else if (hovered) {
            float a = (held ? 0.12f : 0.07f) * (dark_ ? 1.0f : 0.8f);
            dl->AddRectFilled(r.Min, r.Max, dark_ ? u32(ImVec4(1, 1, 1, a)) : u32(ImVec4(0, 0, 0, a)));
            glyph = p.text;
        }
        ImVec2 size = ImGui::CalcTextSize(glyphs[i]);
        dl->AddText(ImVec2(std::floor(x + (w - size.x) / 2), std::floor(origin.y + (h - size.y) / 2)), u32(glyph),
                    glyphs[i]);
        if (hovered) {
            if (hoverButton_ != i) hoverButton_ = i, hoverSince_ = ImGui::GetTime();
            if (!io.MouseDown[0] && ImGui::GetTime() - hoverSince_ > 0.6) {
                ImGui::PushFont(fonts_.body);
                ImGui::SetTooltip("%s", tips[i]);
                ImGui::PopFont();
            }
            anyHovered = true;
        }
        if (clicked) {
            if (i == Minimise) ShowWindow(hwnd_, SW_MINIMIZE);
            if (i == Maximise) ShowWindow(hwnd_, zoomed ? SW_RESTORE : SW_MAXIMIZE);
            if (i == Close) PostMessageW(hwnd_, WM_CLOSE, 0, 0);
        }
    }
    if (!anyHovered) hoverButton_ = -1;
    ImGui::PopFont();
    ImGui::PopClipRect();
    return left;
}

void App::empty(const char* message, bool settingsButton) {
    ImVec2 avail = ImGui::GetContentRegionAvail();
    ImGui::SetCursorPosY(ImGui::GetCursorPosY() + avail.y * 0.35f);
    float w = ImGui::CalcTextSize(message).x;
    ImGui::SetCursorPosX((ImGui::GetWindowWidth() - w) / 2);
    mutedText(message);
    if (client_->State() == ConnState::Offline) {
        std::string err = client_->LastError();
        if (!err.empty()) {
            ImGui::PushFont(fonts_.caption);
            ImGui::SetCursorPosX((ImGui::GetWindowWidth() - ImGui::CalcTextSize(err.c_str()).x) / 2);
            mutedText(err);
            ImGui::PopFont();
        }
    }
    if (settingsButton) {
        ImGui::Dummy(ImVec2(0, 8 * scale_));
        const char* label = "Open settings";
        float bw = ImGui::CalcTextSize(label).x + ImGui::GetStyle().FramePadding.x * 2;
        ImGui::SetCursorPosX((ImGui::GetWindowWidth() - bw) / 2);
        if (ImGui::Button(label)) openSettings();
    }
}

void App::cards(float width) {
    const std::vector<Snapshot> snaps = panels();
    cardRects_.clear();
    if (compact_) {
        compactCards(width);
        dragDrop();
        return;
    }
    float gap = 16 * scale_;
    int cols = std::max(1, int((width + gap) / (440 * scale_ + gap)));
    cols = std::min<int>(cols, int(snaps.size()));
    float colW = (width - gap * (cols - 1)) / cols;

    // Masonry: each card goes in the shortest column so far, using the
    // heights cards had last frame.
    std::vector<std::vector<size_t>> columns(cols);
    std::vector<float> heights(cols, 0);
    for (size_t i = 0; i < snaps.size(); i++) {
        int c = int(std::min_element(heights.begin(), heights.end()) - heights.begin());
        columns[c].push_back(i);
        auto it = heights_.find(snaps[i].monitor);
        heights[c] += (it != heights_.end() ? it->second : 200 * scale_) + gap;
    }
    for (int c = 0; c < cols; c++) {
        if (c > 0) ImGui::SameLine(0, gap);
        ImGui::BeginGroup();
        for (size_t i : columns[c]) {
            card(snaps[i], colW);
            ImGui::Dummy(ImVec2(0, gap - ImGui::GetStyle().ItemSpacing.y * 2));
        }
        ImGui::EndGroup();
    }
    dragDrop();
}

void App::card(const Snapshot& s, float width) {
    const Palette& p = Colours();
    ImGui::PushStyleColor(ImGuiCol_ChildBg, p.card);
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(18 * scale_, 14 * scale_));
    ImGui::BeginChild(s.monitor.c_str(), ImVec2(width, 0),
                      ImGuiChildFlags_Borders | ImGuiChildFlags_AutoResizeY | ImGuiChildFlags_AlwaysUseWindowPadding,
                      ImGuiWindowFlags_NoScrollbar | ImGuiWindowFlags_NoScrollWithMouse);
    ImGui::PopStyleVar();
    bool show = dragging_ == s.monitor || (dragging_.empty() && hoveringCard());
    ImGui::PushFont(fonts_.caption);
    grip(s, ImGui::GetCursorScreenPos().y + ImGui::GetFrameHeight() / 2, show);
    ImGui::PopFont();

    // What's being watched on the left, if the monitor says; when it was
    // updated and refresh on the right. Local panels need neither, and
    // the age only shows on hover, for monitors where it matters now.
    bool local = IsLocal(s);
    if (!local || !s.subtitle.empty()) {
        ImGui::PushFont(fonts_.caption);
        ImGui::AlignTextToFramePadding();
        mutedText(s.subtitle);
        if (!local) {
            bool pending = refreshing_.count(s.monitor) && ImGui::GetTime() - refreshing_[s.monitor] < 15;
            std::string when = pending                     ? "refreshing…"
                               : show && s.timely && s.updated ? "updated " + Ago(s.updated, now())
                                                               : "";
            ImGuiStyle& st = ImGui::GetStyle();
            float bw = ImGui::CalcTextSize(icon::Refresh).x + st.FramePadding.x * 2;
            float ww = ImGui::CalcTextSize(when.c_str()).x;
            ImGui::SameLine(ImGui::GetWindowContentRegionMax().x - bw - ww - st.ItemSpacing.x);
            ImGui::AlignTextToFramePadding();
            mutedText(when);
            ImGui::SameLine();
            if (show) refreshButton(s);
            else ImGui::Dummy(ImVec2(bw, ImGui::GetFrameHeight()));
        }
        ImGui::PopFont();
    }

    if (!s.error.empty()) {
        ImGui::PushFont(fonts_.caption);
        wrapped(std::string(icon::Warning) + "  " + s.error, p.warn);
        ImGui::PopFont();
    }
    if (s.blocks.empty() && s.error.empty()) mutedText("Waiting for the first check…");

    for (size_t i = 0; i < s.blocks.size(); i++) {
        ImGui::PushID(int(i));
        ImGui::Dummy(ImVec2(0, 2 * scale_));
        block(s.monitor, s.blocks[i]);
        ImGui::PopID();
    }

    ImGui::EndChild();
    ImGui::PopStyleColor();
    heights_[s.monitor] = ImGui::GetItemRectSize().y;
    cardRects_.push_back({s.monitor, ImRect(ImGui::GetItemRectMin(), ImGui::GetItemRectMax())});
}

void App::refreshButton(const Snapshot& s) {
    bool pending = refreshing_.count(s.monitor) && ImGui::GetTime() - refreshing_[s.monitor] < 15;
    // Dim, flat until hovered: it's rarely needed.
    ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
    ImGui::PushStyleColor(ImGuiCol_Text, Colours().muted);
    ImGui::PushID(s.monitor.c_str());
    ImGui::BeginDisabled(pending);
    if (ImGui::Button(icon::Refresh)) {
        refreshing_[s.monitor] = ImGui::GetTime();
        client_->Refresh(s.monitor);
    }
    if (ImGui::IsItemHovered(ImGuiHoveredFlags_AllowWhenDisabled)) {
        std::string tip = pending ? "Checking…" : "Check now";
        if (!pending && s.updated) tip += " (updated " + Ago(s.updated, now()) + ")";
        ImGui::SetTooltip("%s", tip.c_str());
    }
    ImGui::EndDisabled();
    ImGui::PopID();
    ImGui::PopStyleColor(2);
}

// compactCards fills the space with one row per monitor, full width so the
// summaries line up, opening as many full panels as fit (see FitPanels).
void App::compactCards(float width) {
    const std::vector<Snapshot> snaps = panels();
    float gap = 8 * scale_;
    float height = ImGui::GetContentRegionAvail().y;

    // Measure every monitor each way at this width, in a window kept behind
    // the main one, whose opaque background hides it and which takes the
    // mouse first. Each copy is drawn at the top of the screen, on top of
    // the others: ImGui stops laying out child windows it can't see, so a
    // copy pushed off the bottom would keep a stale size.
    ImGui::SetNextWindowPos(ImVec2(0, 0));
    ImGui::Begin("##measure", nullptr,
                 ImGuiWindowFlags_NoDecoration | ImGuiWindowFlags_NoInputs | ImGuiWindowFlags_NoNav |
                     ImGuiWindowFlags_NoFocusOnAppearing | ImGuiWindowFlags_NoSavedSettings |
                     ImGuiWindowFlags_NoBackground | ImGuiWindowFlags_AlwaysAutoResize |
                     ImGuiWindowFlags_NoBringToFrontOnFocus);
    ImGui::BringWindowToDisplayBack(ImGui::GetCurrentWindow());
    std::vector<Panel> panels;
    ImVec2 origin = ImGui::GetCursorScreenPos();
    for (const auto& s : snaps) {
        ImGui::PushID(s.monitor.c_str());
        Panel p;
        ImGui::SetCursorScreenPos(origin);
        ImGui::PushID("row");
        p.row = compactCard(s, width, Show::Row, 0, nullptr);
        ImGui::PopID();
        ImGui::SetCursorScreenPos(origin);
        ImGui::PushID("full");
        p.full = compactCard(s, width, Show::Full, 0, nullptr);
        ImGui::PopID();
        ImGui::SetCursorScreenPos(origin);
        ImGui::PushID("clipped");
        p.overhead = compactCard(s, width, Show::Clipped, 1, nullptr) - 1 - p.row;
        ImGui::PopID();
        ImGui::PopID();
        p.pin = config_.expanded.count(s.monitor) ? Pin::Open : config_.collapsed.count(s.monitor) ? Pin::Closed : Pin::Auto;
        panels.push_back(p);
    }
    ImGui::End();

    // A panel only takes the space left over if it shows something useful
    // there: a table's header and four rows. Otherwise it stays a row.
    const ImGuiStyle& st = ImGui::GetStyle();
    float minBody = 5 * (ImGui::GetTextLineHeight() + st.CellPadding.y * 2);
    Fit fit = FitPanels(panels, height, gap, minBody);
    for (size_t i = 0; i < snaps.size(); i++) {
        bool clicked = false;
        compactCard(snaps[i], width, fit.show[i], fit.body, &clicked);
        ImGui::Dummy(ImVec2(0, gap - ImGui::GetStyle().ItemSpacing.y * 2));
        if (!clicked) continue;
        // Open or close it, keeping that choice only while it differs from
        // what would happen by itself, so a pin doesn't outlive its use.
        const std::string& id = snaps[i].monitor;
        bool open = fit.show[i] == Show::Row;
        panels[i].pin = Pin::Auto;
        bool automatic = FitPanels(panels, height, gap, minBody).show[i] != Show::Row;
        config_.expanded.erase(id);
        config_.collapsed.erase(id);
        if (open != automatic) (open ? config_.expanded : config_.collapsed).insert(id);
        SaveConfig(config_);
    }
}

// compactCard draws one monitor: collapsed, a row of its summary; open,
// its full panel. Either way the top line ends in the same two slots,
// refresh then the chevron in the corner, so they line up down the column;
// an open panel keeps that corner to itself. A clipped panel scrolls
// within body. It sets *clicked when the row (or an open panel's corner
// strip) is clicked; with no clicked, it's only being measured and ignores
// the mouse. It returns the card's height.
float App::compactCard(const Snapshot& s, float width, Show show, float body, bool* clicked) {
    const Palette& p = Colours();
    bool open = show != Show::Row;
    ImGui::PushStyleColor(ImGuiCol_ChildBg, p.card);
    // Wide enough at the sides for the grip in the left margin.
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(18 * scale_, 8 * scale_));
    ImGui::BeginChild(s.monitor.c_str(), ImVec2(width, 0),
                      ImGuiChildFlags_Borders | ImGuiChildFlags_AutoResizeY | ImGuiChildFlags_AlwaysUseWindowPadding,
                      ImGuiWindowFlags_NoScrollbar | ImGuiWindowFlags_NoScrollWithMouse);
    ImGui::PopStyleVar();
    ImDrawList* dl = ImGui::GetWindowDrawList();

    ImVec2 top = ImGui::GetCursorScreenPos();
    ImVec2 winMin = ImGui::GetWindowPos();
    float winRight = winMin.x + ImGui::GetWindowWidth();
    float avail = ImGui::GetContentRegionAvail().x;
    const ImGuiStyle& st = ImGui::GetStyle();
    bool local = IsLocal(s);
    bool failed = !s.error.empty();

    // The two slots at the right of the top line, each a button wide.
    ImGui::PushFont(fonts_.caption);
    float lineH = ImGui::GetFrameHeight();
    float slot = ImGui::CalcTextSize(icon::Refresh).x + st.FramePadding.x * 2;
    float cw = ImGui::CalcTextSize(icon::ChevronDown).x;
    ImGui::PopFont();
    float chevronX = top.x + avail - slot;
    float refreshX = chevronX - 2 * scale_ - slot;
    float controlsX = local ? chevronX : refreshX;

    // The grip and buttons only show while the mouse is over the panel (or
    // it's being dragged); their places stay kept so nothing moves.
    bool shown = clicked && (dragging_ == s.monitor || (dragging_.empty() && hoveringCard()));
    bool onButton = grip(s, top.y + lineH / 2, shown);
    auto controls = [&](bool lit) {
        ImGui::PushFont(fonts_.caption);
        if (shown) {
            ImGui::SetCursorScreenPos(ImVec2(chevronX + (slot - cw) / 2, top.y + st.FramePadding.y));
            ImGui::TextColored(lit ? p.text : p.muted, "%s", open ? icon::ChevronUp : icon::ChevronDown);
        }
        if (!local) {
            ImGui::SetCursorScreenPos(ImVec2(refreshX, top.y));
            if (shown) {
                refreshButton(s);
                onButton = onButton || ImGui::IsItemHovered();
            } else {
                ImGui::Dummy(ImVec2(slot, lineH));
            }
        }
        ImGui::PopFont();
    };

    if (!open) {
        dl->ChannelsSplit(2);
        dl->ChannelsSetCurrent(1);
        controls(false);
        // When it was updated, left of the slots; only the server's
        // monitors have one, and it only shows on hover, where it matters
        // now. Its space is kept, so the summary doesn't move.
        float x = controlsX;
        if (!local) {
            bool pending = refreshing_.count(s.monitor) && ImGui::GetTime() - refreshing_[s.monitor] < 15;
            std::string when = failed      ? std::string(icon::Warning) + "  Failed"
                               : pending   ? "refreshing…"
                               : s.updated ? (s.timely ? Ago(s.updated, now()) : "")
                                           : "waiting…";
            bool hidden = !failed && !pending && s.updated && !shown;
            ImGui::PushFont(fonts_.caption);
            if (!when.empty()) x -= ImGui::CalcTextSize(when.c_str()).x + 4 * scale_;
            ImGui::SetCursorScreenPos(ImVec2(x, top.y + st.FramePadding.y));
            if (!when.empty() && !hidden) ImGui::TextColored(failed ? p.warn : p.muted, "%s", when.c_str());
            if (failed && clicked && ImGui::IsItemHovered()) ImGui::SetTooltip("%s", s.error.c_str());
            ImGui::PopFont();
        }
        float rightW = top.x + avail - x + 16 * scale_;
        float bottom = top.y + lineH;

        // On the left: what's being watched, if the monitor says, then the
        // summary, which shares the controls' line when there's no
        // subtitle above it.
        float y = top.y;
        if (!s.subtitle.empty()) {
            ImGui::SetCursorScreenPos(ImVec2(top.x, top.y + st.FramePadding.y));
            ImGui::PushFont(fonts_.caption);
            mutedText(fit(s.subtitle, avail - rightW));
            ImGui::PopFont();
            y = bottom;
        }
        ImGui::SetCursorScreenPos(ImVec2(top.x, y));
        if (!s.summary.empty()) {
            float w = avail - (y == top.y ? rightW : 0);
            if (s.summaryStyle == "chips") chips(s.summary, w);
            else summary(s.summary, w);
        } else if (s.blocks.empty() && !failed) {
            mutedText("Waiting for the first check…");
        } else {
            mutedText("Click for details");
        }
        bottom = std::max(bottom, ImGui::GetItemRectMax().y);

        // Clicking anywhere on the row, bar the buttons and grip, opens it.
        ImVec2 hitMin = winMin, hitMax(winRight, bottom + 8 * scale_);
        dl->ChannelsSetCurrent(0);
        bool hovered = clicked && dragging_.empty() && ImGui::IsWindowHovered() &&
                       ImGui::IsMouseHoveringRect(hitMin, hitMax) && !onButton;
        if (hovered) {
            ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
            dl->AddRectFilled(hitMin, hitMax, u32(p.hover));
            if (ImGui::IsMouseClicked(ImGuiMouseButton_Left)) *clicked = true;
        }
        dl->ChannelsMerge();
        ImGui::SetCursorScreenPos(ImVec2(top.x, bottom));
        ImGui::Dummy(ImVec2(avail, 0));
    } else {
        // The panel takes the width bar a strip down the right holding the
        // slots; clicking the strip closes it.
        float strip = top.x + avail - controlsX + 8 * scale_;
        ImVec2 hitMin(controlsX - 4 * scale_, winMin.y), hitMax(winRight, winMin.y + ImGui::GetWindowHeight());
        bool inStrip = clicked && dragging_.empty() && ImGui::IsWindowHovered() &&
                       ImGui::IsMouseHoveringRect(hitMin, hitMax);
        controls(inStrip && !onButton);
        if (inStrip && !onButton) {
            ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
            if (ImGui::IsMouseClicked(ImGuiMouseButton_Left)) *clicked = true;
        }

        ImGui::SetCursorScreenPos(top);
        bool clip = show == Show::Clipped;
        ImGui::BeginChild("##body", ImVec2(avail - strip, clip ? body : 0),
                          clip ? ImGuiChildFlags_None : ImGuiChildFlags_AutoResizeY,
                          ImGuiWindowFlags_NoBackground |
                              (clip ? 0 : ImGuiWindowFlags_NoScrollbar | ImGuiWindowFlags_NoScrollWithMouse));
        if (!s.subtitle.empty()) {
            ImGui::PushFont(fonts_.caption);
            wrapped(s.subtitle, p.muted);
            ImGui::PopFont();
        }
        if (failed) {
            ImGui::PushFont(fonts_.caption);
            wrapped(std::string(icon::Warning) + "  " + s.error, p.warn);
            ImGui::PopFont();
        }
        for (size_t i = 0; i < s.blocks.size(); i++) {
            ImGui::PushID(int(i));
            if (i > 0) ImGui::Dummy(ImVec2(0, 2 * scale_));
            block(s.monitor, s.blocks[i]);
            ImGui::PopID();
        }
        ImGui::EndChild();
        float bottom = std::max(ImGui::GetItemRectMax().y, top.y + lineH);
        ImGui::SetCursorScreenPos(ImVec2(top.x, bottom));
        ImGui::Dummy(ImVec2(avail, 0));
    }
    ImGui::EndChild();
    ImGui::PopStyleColor();
    if (clicked) cardRects_.push_back({s.monitor, ImRect(ImGui::GetItemRectMin(), ImGui::GetItemRectMax())});
    return ImGui::GetItemRectSize().y;
}

// grip draws the six dots that drag a panel, in the card's left margin,
// centred on y, and starts a drag from them; when not shown it does
// nothing. It reports whether the mouse is over them.
bool App::grip(const Snapshot& s, float y, bool shown) {
    if (!shown) return false;
    const Palette& p = Colours();
    ImVec2 winMin = ImGui::GetWindowPos();
    float dot = std::max(2.0f, std::round(2 * scale_)), step = dot * 2;
    float x = winMin.x + std::round(6 * scale_), h = step * 2 + dot;
    ImRect hit(ImVec2(winMin.x, y - h / 2 - 4 * scale_), ImVec2(x + step + dot + 3 * scale_, y + h / 2 + 4 * scale_));

    // The margin is outside the window's clip rect, so widen it.
    ImGui::PushClipRect(winMin, ImVec2(winMin.x + ImGui::GetWindowWidth(), winMin.y + ImGui::GetWindowHeight()), false);
    bool hovered = false;
    bool dragging = dragging_ == s.monitor;
    {
        ImVec2 saved = ImGui::GetCursorScreenPos();
        ImGui::SetCursorScreenPos(hit.Min);
        ImGui::PushID("##grip");
        ImGui::InvisibleButton("##grip", hit.GetSize());
        ImGui::PopID();
        hovered = ImGui::IsItemHovered();
        if (ImGui::IsItemActive() && ImGui::IsMouseDragging(ImGuiMouseButton_Left, 3)) dragging_ = s.monitor;
        if (hovered && !dragging) ImGui::SetTooltip("Drag to move");
        if (hovered || dragging) ImGui::SetMouseCursor(ImGuiMouseCursor_ResizeAll);
        ImGui::SetCursorScreenPos(saved);
    }
    ImVec4 colour = hovered || dragging ? p.text : fade(p.muted, 0.6f);
    ImDrawList* dl = ImGui::GetWindowDrawList();
    for (int row = 0; row < 3; row++)
        for (int col = 0; col < 2; col++) {
            ImVec2 a(x + col * step, std::round(y - h / 2 + row * step));
            dl->AddRectFilled(a, ImVec2(a.x + dot, a.y + dot), u32(colour));
        }
    ImGui::PopClipRect();
    return hovered;
}

// dragDrop shows where a dragged panel will land, before or after the card
// nearest the mouse, and moves it there on release.
void App::dragDrop() {
    if (dragging_.empty()) return;
    const Palette& p = Colours();
    ImVec2 m = ImGui::GetMousePos();
    const std::pair<std::string, ImRect>* target = nullptr;
    const ImRect* dragged = nullptr;
    float best = FLT_MAX;
    for (const auto& c : cardRects_) {
        if (c.first == dragging_) dragged = &c.second;
        float dx = std::max({c.second.Min.x - m.x, 0.0f, m.x - c.second.Max.x});
        float dy = std::max({c.second.Min.y - m.y, 0.0f, m.y - c.second.Max.y});
        if (dx * dx + dy * dy < best) best = dx * dx + dy * dy, target = &c;
    }
    ImDrawList* fg = ImGui::GetForegroundDrawList();
    if (dragged) fg->AddRectFilled(dragged->Min, dragged->Max, u32(fade(p.bg, 0.6f)));
    bool after = false;
    if (target) {
        const ImRect& r = target->second;
        after = m.y > (r.Min.y + r.Max.y) / 2;
        float y = after ? r.Max.y + 4 * scale_ : r.Min.y - 4 * scale_;
        float t = std::max(2.0f, std::round(2 * scale_));
        fg->AddRectFilled(ImVec2(r.Min.x, y - t / 2), ImVec2(r.Max.x, y + t / 2), u32(p.accent));
    }
    ImGui::SetMouseCursor(ImGuiMouseCursor_ResizeAll);
    if (ImGui::IsMouseDown(ImGuiMouseButton_Left)) return;

    // Dropped.
    if (target) {
        std::vector<std::string> order;
        for (const auto& s : panels()) order.push_back(s.monitor);
        config_.order = Move(order, dragging_, target->first, after);
        SaveConfig(config_);
    }
    dragging_.clear();
}

// summary lays stats out as tiles of label, value and detail, flowing onto
// more lines when they don't fit in width.
// chips lays stats out as small blocks filled with their tone's colour:
// the label (which may be several lines) above the value and any detail
// below, all one size so they line up. If they nearly fit on one line,
// they narrow to do so, shortening text that's too wide; otherwise they
// flow onto more lines.
void App::chips(const std::vector<Stat>& stats, float width) {
    if (stats.empty()) return;
    const Palette& p = Colours();
    ImDrawList* dl = ImGui::GetWindowDrawList();
    ImVec2 pad(6 * scale_, 4 * scale_);
    float gap = 4 * scale_;

    // One size for all, from the widest and tallest of each part.
    float w = 0, labelH = 0, valueH = 0, detailH = 0;
    for (const Stat& s : stats) {
        ImGui::PushFont(fonts_.caption);
        ImVec2 l = ImGui::CalcTextSize(s.label.c_str());
        w = std::max(w, l.x);
        labelH = std::max(labelH, l.y);
        if (!s.detail.empty()) {
            ImVec2 d = ImGui::CalcTextSize(s.detail.c_str());
            w = std::max(w, d.x);
            detailH = std::max(detailH, d.y);
        }
        ImGui::PopFont();
        ImGui::PushFont(fonts_.heading);
        ImVec2 v = ImGui::CalcTextSize(s.value.c_str());
        w = std::max(w, v.x);
        valueH = std::max(valueH, v.y);
        ImGui::PopFont();
    }
    w += pad.x * 2;
    float oneRow = (width - gap * float(stats.size() - 1)) / float(stats.size());
    if (w > oneRow && oneRow >= 44 * scale_) w = oneRow;
    w = std::min(w, width);
    float inner = w - pad.x * 2;
    float h = pad.y * 2 + labelH + valueH + detailH;

    ImVec2 origin = ImGui::GetCursorScreenPos();
    int perRow = std::max(1, int((width + gap) / (w + gap)));
    ImGui::BeginGroup();
    for (size_t i = 0; i < stats.size(); i++) {
        const Stat& s = stats[i];
        ImVec2 a(origin.x + float(i % perRow) * (w + gap), origin.y + float(i / perRow) * (h + gap));
        ImVec4 tone = ToneColour(s.tone);
        bool coloured = s.tone == Tone::Good || s.tone == Tone::Bad || s.tone == Tone::Warn || s.tone == Tone::Accent;
        dl->AddRectFilled(a, ImVec2(a.x + w, a.y + h), u32(coloured ? fade(tone, 0.2f) : p.frame));
        ImGui::PushFont(fonts_.caption);
        ImGui::SetCursorScreenPos(ImVec2(a.x + pad.x, a.y + pad.y));
        ImGui::TextColored(s.tone == Tone::Muted ? p.muted : p.text, "%s", fitLines(s.label, inner).c_str());
        ImGui::PopFont();
        ImGui::PushFont(fonts_.heading);
        ImGui::SetCursorScreenPos(ImVec2(a.x + pad.x, a.y + pad.y + labelH));
        ImGui::TextColored(coloured ? tone : s.tone == Tone::Muted ? p.muted : p.text, "%s", fit(s.value, inner).c_str());
        ImGui::PopFont();
        if (!s.detail.empty()) {
            ImGui::PushFont(fonts_.caption);
            ImGui::SetCursorScreenPos(ImVec2(a.x + pad.x, a.y + pad.y + labelH + valueH));
            ImGui::TextColored(p.muted, "%s", fitLines(s.detail, inner).c_str());
            ImGui::PopFont();
        }
        // The block itself, for the group's size.
        ImGui::SetCursorScreenPos(a);
        ImGui::Dummy(ImVec2(w, h));
    }
    ImGui::EndGroup();
}

void App::summary(const std::vector<Stat>& stats, float width) {
    bool details = std::any_of(stats.begin(), stats.end(), [](const Stat& s) { return !s.detail.empty(); });
    float spacing = 24 * scale_;
    ImVec2 origin = ImGui::GetCursorScreenPos();
    float x = 0, y = 0, lineH = 0;
    ImGui::BeginGroup();
    for (size_t i = 0; i < stats.size(); i++) {
        const Stat& s = stats[i];
        ImGui::PushFont(fonts_.caption);
        std::string label = fit(s.label, width), detail = fit(s.detail, width);
        float w = std::max(ImGui::CalcTextSize(label.c_str()).x, ImGui::CalcTextSize(detail.c_str()).x);
        ImGui::PopFont();
        ImGui::PushFont(fonts_.heading);
        std::string value = fit(s.value, width);
        w = std::max(w, ImGui::CalcTextSize(value.c_str()).x);
        ImGui::PopFont();

        if (x > 0 && x + w > width) x = 0, y += lineH + 4 * scale_, lineH = 0;
        ImGui::SetCursorScreenPos(ImVec2(origin.x + x, origin.y + y));
        ImGui::BeginGroup();
        ImGui::PushFont(fonts_.caption);
        mutedText(label);
        ImGui::PopFont();
        ImGui::PushFont(fonts_.heading);
        ImGui::TextColored(ToneColour(s.tone), "%s", value.c_str());
        ImGui::PopFont();
        if (details) {
            ImGui::PushFont(fonts_.caption);
            mutedText(detail.empty() ? " " : detail);
            ImGui::PopFont();
        }
        ImGui::EndGroup();
        lineH = std::max(lineH, ImGui::GetItemRectSize().y);
        x += w + spacing;
    }
    ImGui::EndGroup();
}

void App::block(const std::string& monitor, const Block& b) {
    if (!b.title.empty() && b.type != "stats") {
        ImGui::PushFont(fonts_.caption);
        mutedText(b.title);
        ImGui::PopFont();
    }
    if (b.type == "stats") stats(b);
    else if (b.type == "table") table(b);
    else if (b.type == "list") list(b);
    else if (b.type == "checklist") checklist(monitor, b);
    else if (b.type == "media") mediaBlock(b);
    else if (!b.text.empty()) wrapped(b.text, Colours().text);
}

// mediaBlock draws what's playing: the cover, the track, a progress bar
// that moves on between readings, and previous, play/pause and next.
void App::mediaBlock(const Block& b) {
    if (b.items.empty()) return;
    const Item& it = b.items[0];
    const Media& m = b.media;
    const Palette& p = Colours();
    ImDrawList* dl = ImGui::GetWindowDrawList();
    float art = 76 * scale_, gap = 12 * scale_;

    ImVec2 at = ImGui::GetCursorScreenPos();
    float avail = ImGui::GetContentRegionAvail().x;
    ImVec2 to(at.x + art, at.y + art);
    if (const Texture* t = images_->Get(it.image)) {
        // Crop to a square rather than squash.
        float ta = float(t->width) / float(t->height);
        ImVec2 uv0(0, 0), uv1(1, 1);
        if (ta > 1) uv0.x = (1 - 1 / ta) / 2, uv1.x = 1 - uv0.x;
        else uv0.y = (1 - ta) / 2, uv1.y = 1 - uv0.y;
        dl->AddImage((ImTextureID)(intptr_t)t->srv, at, to, uv0, uv1);
    } else {
        dl->AddRectFilled(at, to, u32(p.frame));
    }
    ImGui::Dummy(ImVec2(art, art));
    ImGui::SameLine(0, gap);

    float w = avail - art - gap;
    ImGui::BeginGroup();
    ImGui::PushFont(fonts_.heading);
    ImGui::TextUnformatted(fit(it.title, w).c_str());
    ImGui::PopFont();
    if (!it.subtitle.empty()) ImGui::TextUnformatted(fit(it.subtitle, w).c_str());
    ImGui::PushFont(fonts_.caption);
    if (!it.detail.empty()) mutedText(fit(it.detail, w));

    // Progress, moved on by the time since Windows last said where it was.
    if (m.duration > 0) {
        double now = std::chrono::duration<double>(std::chrono::system_clock::now().time_since_epoch()).count();
        double pos = std::clamp(m.position + (m.playing ? now - m.asOf : 0), 0.0, m.duration);
        std::string times = FormatTime(pos) + " / " + FormatTime(m.duration);
        float tw = ImGui::CalcTextSize(times.c_str()).x;
        float barW = std::max(w - tw - gap, 20 * scale_);
        ImVec2 bar = ImGui::GetCursorScreenPos();
        float y = std::round(bar.y + ImGui::GetTextLineHeight() / 2);
        float h = std::max(2.0f, std::round(3 * scale_));
        dl->AddRectFilled(ImVec2(bar.x, y - h / 2), ImVec2(bar.x + barW, y + h / 2), u32(p.frame));
        dl->AddRectFilled(ImVec2(bar.x, y - h / 2), ImVec2(bar.x + barW * float(pos / m.duration), y + h / 2),
                          u32(m.playing ? p.accent : p.muted));
        ImGui::Dummy(ImVec2(barW, ImGui::GetTextLineHeight()));
        ImGui::SameLine(0, gap);
        mutedText(times);
    }
    ImGui::PopFont();

    // Controls, flat and dim until hovered, like refresh.
    ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
    ImGui::PushStyleColor(ImGuiCol_Text, p.muted);
    auto control = [&](const char* glyph, bool enabled, MediaCommand c, const char* tip) {
        ImGui::BeginDisabled(!enabled);
        if (ImGui::Button(glyph)) media_->Command(c);
        if (ImGui::IsItemHovered()) ImGui::SetTooltip("%s", tip);
        ImGui::EndDisabled();
    };
    control(icon::Previous, m.canPrevious, MediaCommand::Previous, "Previous");
    ImGui::SameLine(0, 2 * scale_);
    control(m.playing ? icon::Pause : icon::Play, m.canToggle, MediaCommand::TogglePlay, m.playing ? "Pause" : "Play");
    ImGui::SameLine(0, 2 * scale_);
    control(icon::Next, m.canNext, MediaCommand::Next, "Next");
    ImGui::PopStyleColor(2);
    ImGui::EndGroup();
}

// checklist draws items with checkboxes, a delete button on the one under
// the mouse, and a box for new items if the block takes them. Each change is
// sent to the monitor as an action; ticks show at once, ahead of the
// server's snapshot.
void App::checklist(const std::string& monitor, const Block& b) {
    const Palette& p = Colours();
    const ImGuiStyle& st = ImGui::GetStyle();
    auto act = [&](nlohmann::json action) { client_->Act(monitor, action.dump()); };
    float right = ImGui::GetCursorScreenPos().x + ImGui::GetContentRegionAvail().x;
    ImGui::PushFont(fonts_.caption);
    float bw = ImGui::CalcTextSize(icon::Close).x + st.FramePadding.x * 2;
    ImGui::PopFont();

    for (const Item& it : b.items) {
        ImGui::PushID(it.id.c_str());
        std::string key = monitor + "/" + it.id;
        bool done = it.done;
        if (auto t = ticked_.find(key); t != ticked_.end() && ImGui::GetTime() - t->second.second < 10)
            done = t->second.first;

        ImVec2 rowMin = ImGui::GetCursorScreenPos();
        bool toggle = false;
        bool v = done;
        if (ImGui::Checkbox("##done", &v)) toggle = true;
        ImGui::SameLine();
        // The text wraps short of the delete button, and clicking it ticks
        // the box too.
        ImGui::PushTextWrapPos(right - bw - st.ItemSpacing.x - ImGui::GetWindowPos().x);
        ImGui::AlignTextToFramePadding();
        ImGui::TextColored(done ? p.muted : p.text, "%s", it.title.c_str());
        ImGui::PopTextWrapPos();
        ImVec2 textMin = ImGui::GetItemRectMin(), textMax = ImGui::GetItemRectMax();
        if (ImGui::IsItemHovered()) ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
        if (ImGui::IsItemClicked()) toggle = true;
        if (done && textMax.y - textMin.y < ImGui::GetTextLineHeight() * 1.5f) {
            float y = std::round((textMin.y + textMax.y) / 2);
            ImGui::GetWindowDrawList()->AddLine(ImVec2(textMin.x, y), ImVec2(textMax.x, y), u32(p.muted));
        }
        ImVec2 next = ImGui::GetCursorScreenPos();
        float rowBottom = std::max(textMax.y, ImGui::GetItemRectMax().y);

        // Allowing for an active item keeps the button there while it's
        // held, so the click completes.
        if (ImGui::IsWindowHovered(ImGuiHoveredFlags_AllowWhenBlockedByActiveItem) &&
            ImGui::IsMouseHoveringRect(rowMin, ImVec2(right, rowBottom))) {
            ImGui::SetCursorScreenPos(ImVec2(right - bw, rowMin.y));
            ImGui::PushFont(fonts_.caption);
            ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
            ImGui::PushStyleColor(ImGuiCol_Text, p.muted);
            if (ImGui::Button(icon::Close)) act({{"type", "delete"}, {"id", it.id}});
            if (ImGui::IsItemHovered()) ImGui::SetTooltip("Delete");
            ImGui::PopStyleColor(2);
            ImGui::PopFont();
            ImGui::SetCursorScreenPos(next);
        }
        if (toggle) {
            ticked_[key] = {!done, ImGui::GetTime()};
            act({{"type", "toggle"}, {"id", it.id}});
        }
        ImGui::PopID();
    }
    if (b.items.empty()) mutedText("Nothing to do.");

    if (!b.addHint.empty()) {
        auto& buf = newItem_[monitor];
        ImGui::Dummy(ImVec2(0, 2 * scale_));
        ImGui::SetNextItemWidth(right - ImGui::GetCursorScreenPos().x);
        if (ImGui::InputTextWithHint("##add", b.addHint.c_str(), buf.data(), buf.size(),
                                     ImGuiInputTextFlags_EnterReturnsTrue)) {
            std::string text = buf.data();
            if (text.find_first_not_of(" \t") != std::string::npos) act({{"type", "add"}, {"text", text}});
            buf[0] = 0;
            ImGui::SetKeyboardFocusHere(-1);  // stay in the box for the next one
        }
    }
}

void App::stats(const Block& b) {
    if (!b.title.empty()) {
        ImGui::PushFont(fonts_.caption);
        mutedText(b.title);
        ImGui::PopFont();
    }
    size_t n = b.stats.size();
    if (n == 0) return;
    float avail = ImGui::GetContentRegionAvail().x;
    float spacing = 12 * scale_;
    size_t perRow = std::clamp<size_t>(size_t((avail + spacing) / (96 * scale_ + spacing)), 1, n);
    float tileW = (avail - spacing * (perRow - 1)) / perRow;
    for (size_t i = 0; i < n; i++) {
        const Stat& s = b.stats[i];
        if (i % perRow != 0) ImGui::SameLine(0, spacing);
        ImGui::BeginGroup();
        ImGui::PushFont(fonts_.caption);
        mutedText(s.label);
        ImGui::PopFont();
        ImGui::PushFont(fonts_.big);
        ImGui::TextColored(ToneColour(s.tone), "%s", s.value.c_str());
        ImGui::PopFont();
        // A flat series, e.g. no rain at all, says nothing worth a chart,
        // unless its scale is fixed: memory steady at 50% still shows.
        bool flat = s.series.empty() ||
                    *std::max_element(s.series.begin(), s.series.end()) == *std::min_element(s.series.begin(), s.series.end());
        if (!flat || (s.top > 0 && !s.series.empty())) {
            sparkline(s.series, ImVec2(tileW, 22 * scale_), s.tone == Tone::None ? Colours().accent : ToneColour(s.tone),
                      s.top);
        }
        ImGui::PushFont(fonts_.caption);
        mutedText(s.detail.empty() ? " " : s.detail);
        ImGui::PopFont();
        ImGui::Dummy(ImVec2(tileW, 0));
        ImGui::EndGroup();
    }
    ImGui::Dummy(ImVec2(0, 4 * scale_));
}

void App::table(const Block& b) {
    size_t cols = b.columns.size();
    if (cols == 0) return;
    // The column marked fill takes the spare width, or failing that the
    // left-aligned column with the most text. The rest fit their contents.
    auto fill = std::find_if(b.columns.begin(), b.columns.end(), [](const Column& c) { return c.fill; });
    int stretch = fill != b.columns.end() ? int(fill - b.columns.begin()) : -1;
    size_t most = 0;
    for (size_t c = 0; c < cols && fill == b.columns.end(); c++) {
        if (b.columns[c].align != Align::Left) continue;
        size_t total = 0;
        for (const auto& r : b.rows)
            if (c < r.size()) total += r[c].text.size();
        if (stretch < 0 || total > most) stretch = int(c), most = total;
    }

    ImGuiTableFlags flags = ImGuiTableFlags_SizingFixedFit | ImGuiTableFlags_RowBg | ImGuiTableFlags_NoPadOuterX;
    if (!ImGui::BeginTable("##table", int(cols), flags)) return;
    for (size_t c = 0; c < cols; c++)
        ImGui::TableSetupColumn(b.columns[c].title.c_str(),
                                int(c) == stretch ? ImGuiTableColumnFlags_WidthStretch : ImGuiTableColumnFlags_WidthFixed);

    bool headers = std::any_of(b.columns.begin(), b.columns.end(), [](const Column& c) { return !c.title.empty(); });
    if (headers) {
        ImGui::TableNextRow(ImGuiTableRowFlags_Headers);
        ImGui::PushFont(fonts_.caption);
        for (size_t c = 0; c < cols; c++) {
            ImGui::TableSetColumnIndex(int(c));
            textAligned(b.columns[c].title, b.columns[c].align, Colours().muted);
        }
        ImGui::PopFont();
    }
    for (const auto& row : b.rows) {
        ImGui::TableNextRow();
        for (size_t c = 0; c < cols && c < row.size(); c++) {
            ImGui::TableSetColumnIndex(int(c));
            const Cell& cell = row[c];
            textAligned(cell.text, b.columns[c].align, ToneColour(cell.tone));
            linkBehaviour(cell.url);
            if (!cell.detail.empty()) {
                ImGui::PushFont(fonts_.caption);
                if (b.columns[c].align == Align::Left) wrapped(cell.detail, Colours().muted);
                else textAligned(cell.detail, b.columns[c].align, Colours().muted);
                ImGui::PopFont();
            }
        }
    }
    ImGui::EndTable();
}

void App::list(const Block& b) {
    const Palette& p = Colours();
    float thumbW = 112 * scale_, thumbH = 84 * scale_;
    float pad = 6 * scale_;
    ImDrawList* dl = ImGui::GetWindowDrawList();
    for (size_t i = 0; i < b.items.size(); i++) {
        const Item& it = b.items[i];
        ImGui::PushID(int(i));
        dl->ChannelsSplit(2);
        dl->ChannelsSetCurrent(1);

        ImVec2 start = ImGui::GetCursorScreenPos();
        float right = start.x + ImGui::GetContentRegionAvail().x;
        ImGui::SetCursorScreenPos(ImVec2(start.x + pad, start.y + pad));
        ImGui::BeginGroup();
        if (!it.image.empty()) {
            ImVec2 tp = ImGui::GetCursorScreenPos();
            ImVec2 tq(tp.x + thumbW, tp.y + thumbH);
            if (const Texture* t = images_->Get(it.image)) {
                // Crop to the thumbnail's shape rather than squash.
                float ta = float(t->width) / float(t->height), ba = thumbW / thumbH;
                ImVec2 uv0(0, 0), uv1(1, 1);
                if (ta > ba) uv0.x = (1 - ba / ta) / 2, uv1.x = 1 - uv0.x;
                else uv0.y = (1 - ta / ba) / 2, uv1.y = 1 - uv0.y;
                dl->AddImage((ImTextureID)(intptr_t)t->srv, tp, tq, uv0, uv1);
            } else {
                dl->AddRectFilled(tp, tq, u32(p.frame));
            }
            ImGui::Dummy(ImVec2(thumbW, thumbH));
            ImGui::SameLine(0, 12 * scale_);
        }
        ImGui::BeginGroup();
        ImGui::PushTextWrapPos(right - pad - ImGui::GetWindowPos().x);
        ImGui::PushFont(fonts_.heading);
        ImGui::TextUnformatted(it.title.c_str());
        ImGui::PopFont();
        if (!it.subtitle.empty()) wrapped(it.subtitle, it.tone == Tone::None ? p.text : ToneColour(it.tone));
        if (!it.detail.empty()) {
            ImGui::PushFont(fonts_.caption);
            wrapped(it.detail, p.muted);
            ImGui::PopFont();
        }
        ImGui::PopTextWrapPos();
        ImGui::EndGroup();
        ImGui::EndGroup();

        ImVec2 end(right, ImGui::GetItemRectMax().y + pad);
        ImGui::SetCursorScreenPos(start);
        ImGui::InvisibleButton("##item", ImVec2(right - start.x, end.y - start.y));
        dl->ChannelsSetCurrent(0);
        if (ImGui::IsItemHovered() && !it.url.empty()) {
            dl->AddRectFilled(start, end, u32(p.hover));
            ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
        }
        if (ImGui::IsItemClicked()) OpenURL(it.url);
        dl->ChannelsMerge();
        ImGui::PopID();
    }
}

namespace {

// DrawListCanvas draws the cat with ImGui.
class DrawListCanvas : public Canvas {
public:
    explicit DrawListCanvas(ImDrawList* dl) : dl_(dl) {}
    void Circle(Vec2f c, float r, Rgba col) override { dl_->AddCircleFilled(v(c), r, u(col)); }
    void Ellipse(Vec2f c, float rx, float ry, float rot, Rgba col) override {
        dl_->AddEllipseFilled(v(c), ImVec2(rx, ry), u(col), rot);
    }
    void Line(Vec2f a, Vec2f b, float thickness, Rgba col) override { dl_->AddLine(v(a), v(b), u(col), thickness); }
    void Polygon(const Vec2f* points, int n, Rgba col) override {
        ImVec2 pts[16];
        n = std::min(n, 16);
        for (int i = 0; i < n; i++) pts[i] = v(points[i]);
        dl_->AddConvexPolyFilled(pts, n, u(col));
    }

private:
    static ImVec2 v(Vec2f p) { return ImVec2(p.x, p.y); }
    static ImU32 u(Rgba c) { return IM_COL32(c.r, c.g, c.b, c.a); }
    ImDrawList* dl_;
};

}  // namespace

// cat draws the black cat along the top of the Recent column.
void App::cat(float width) {
    float size = 1.1f * scale_;
    // Her space is 68 units tall: 64 above the floor (an arched tail),
    // 4 below (rounded paws and her rim).
    float h = std::round(68 * size);
    ImVec2 at = ImGui::GetCursorScreenPos();
    ImGui::Dummy(ImVec2(width, h));
    double t = ImGui::GetTime();
    Cat c = CatAt(t, width / 2 / size);
    DrawListCanvas canvas(ImGui::GetWindowDrawList());
    DrawCat(canvas, c, t, Vec2f{at.x + width / 2, at.y + h - 4 * size}, size);
    // Asleep, her breathing and Z's are slow: ten frames a second will do.
    catInterval_ = c.asleep >= 1 ? 100 : 33;
}

// overlayScrollbar draws a thin scrollbar over the right edge of the
// current child window while the mouse is over it, in place of ImGui's,
// which takes width from the content all the time. It can be dragged, or
// clicked to jump. Call it last, before EndChild.
void App::overlayScrollbar() {
    const ImGuiIO& io = ImGui::GetIO();
    ImGuiWindow* w = ImGui::GetCurrentWindow();
    float maxY = ImGui::GetScrollMaxY();
    ImRect r = w->InnerRect;
    feedBar_ = ImRect();
    if (maxY <= 0) {
        feedBarHeld_ = false;
        return;
    }
    float track = r.GetHeight();
    float thumbH = std::max(32 * scale_, track * track / (track + maxY));
    float thumbY = r.Min.y + (track - thumbH) * ImGui::GetScrollY() / maxY;
    float barW = 6 * scale_, inset = 2 * scale_;
    ImRect thumb(ImVec2(r.Max.x - inset - barW, thumbY), ImVec2(r.Max.x - inset, thumbY + thumbH));
    ImRect strip(ImVec2(r.Max.x - inset - barW - 6 * scale_, r.Min.y), r.Max);

    bool overContent = ImGui::IsWindowHovered(ImGuiHoveredFlags_ChildWindows | ImGuiHoveredFlags_AllowWhenBlockedByActiveItem);
    bool overStrip = overContent && strip.Contains(io.MousePos);
    if (feedBarHeld_) {
        if (!io.MouseDown[0]) feedBarHeld_ = false;
        else ImGui::SetScrollY(std::clamp((io.MousePos.y - feedBarGrab_ - r.Min.y) / (track - thumbH), 0.0f, 1.0f) * maxY);
    } else if (overStrip && io.MouseClicked[0]) {
        // On the thumb, drag it from where it was caught; elsewhere, jump
        // there and drag from its middle.
        feedBarGrab_ = thumb.Contains(io.MousePos) ? io.MousePos.y - thumb.Min.y : thumbH / 2;
        feedBarHeld_ = true;
    }
    if (overContent || feedBarHeld_) {
        const Palette& p = Colours();
        w->DrawList->AddRectFilled(thumb.Min, thumb.Max, u32(overStrip || feedBarHeld_ ? p.muted : p.border), barW / 2);
        feedBar_ = strip;
    }
}

// feedHeading is the Recent column's heading, above its scrolling cards,
// with an x at the right to clear them all while the mouse is over the
// column.
void App::feedHeading(float width) {
    const Palette& p = Colours();
    const ImGuiIO& io = ImGui::GetIO();
    ImVec2 at = ImGui::GetCursorScreenPos();
    ImGui::PushFont(fonts_.heading);
    ImGui::TextUnformatted("Recent");
    ImGui::PopFont();
    float h = ImGui::GetItemRectSize().y;
    ImGui::Dummy(ImVec2(0, 2 * scale_));
    if (model_.Events().empty() || !feedRect_.Contains(io.MousePos)) return;

    float xs = std::round(22 * scale_);
    ImRect x(ImVec2(at.x + width - xs, std::round(at.y + (h - xs) / 2)), ImVec2(at.x + width, std::round(at.y + (h + xs) / 2)));
    bool over = x.Contains(io.MousePos) && ImGui::IsWindowHovered(ImGuiHoveredFlags_ChildWindows);
    ImDrawList* dl = ImGui::GetWindowDrawList();
    if (over) dl->AddRectFilled(x.Min, x.Max, u32(p.frame), 4 * scale_);
    ImGui::PushFont(fonts_.chrome);
    ImVec2 g = ImGui::CalcTextSize(icon::Close);
    dl->AddText(ImVec2(std::round(x.GetCenter().x - g.x / 2), std::round(x.GetCenter().y - g.y / 2)),
                u32(over ? p.text : p.muted), icon::Close);
    ImGui::PopFont();
    if (!over) return;
    ImGui::SetTooltip("Clear all");
    if (io.MouseClicked[0]) {
        model_.ClearAll();
        config_.cleared = model_.GetCleared();
        SaveConfig(config_);
    }
}

void App::feed() {
    const Palette& p = Colours();
    const ImGuiIO& io = ImGui::GetIO();
    const auto& events = model_.Events();
    if (events.empty()) {
        mutedText("Nothing new.");
        return;
    }
    ImDrawList* dl = ImGui::GetWindowDrawList();
    float pad = (compact_ ? 7 : 10) * scale_;
    int64_t t = now();
    size_t shown = 0;
    // Clicks on the scrollbar aren't for the card under it.
    bool onBar = feedBarHeld_ || feedBar_.Contains(io.MousePos);
    int64_t clear = 0;
    for (const auto& e : events) {
        if (++shown > 150) break;
        ImGui::PushID(int(e.id));
        dl->ChannelsSplit(2);
        dl->ChannelsSetCurrent(1);
        ImVec2 start = ImGui::GetCursorScreenPos();
        float width = ImGui::GetContentRegionAvail().x;
        ImGui::SetCursorScreenPos(ImVec2(start.x + pad, start.y + pad * 0.8f));
        ImGui::BeginGroup();
        ImGui::PushTextWrapPos(start.x + width - pad - ImGui::GetWindowPos().x);
        ImGui::PushFont(fonts_.caption);
        mutedText(model_.TitleOf(e.monitor) + "  ·  " + Ago(e.time, t));
        ImGui::PopFont();
        ImGui::PushFont(e.quiet ? fonts_.body : fonts_.heading);
        wrapped(e.title, e.quiet ? p.muted : p.text);
        ImGui::PopFont();
        if (!e.body.empty()) {
            ImGui::PushFont(fonts_.caption);
            wrapped(e.body, p.muted);
            ImGui::PopFont();
        }
        ImGui::PopTextWrapPos();
        ImGui::EndGroup();
        ImVec2 end(start.x + width, ImGui::GetItemRectMax().y + pad * 0.8f);
        ImGui::SetCursorScreenPos(start);
        ImGui::InvisibleButton("##event", ImVec2(width, end.y - start.y));

        bool over = ImGui::IsItemHovered() && !onBar;
        // An x in the top-right corner, while the card's hovered, clears it.
        float xs = std::round(22 * scale_);
        // Clear of the scrollbar, which shows over the right edge on hover.
        float edge = end.x - 15 * scale_;
        ImRect x(ImVec2(edge - xs, start.y + 3 * scale_), ImVec2(edge, start.y + 3 * scale_ + xs));
        bool overX = over && x.Contains(io.MousePos);
        if (over) {
            if (overX) dl->AddRectFilled(x.Min, x.Max, u32(p.frame), 4 * scale_);
            ImGui::PushFont(fonts_.chrome);
            ImVec2 g = ImGui::CalcTextSize(icon::Close);
            dl->AddText(ImVec2(std::round(x.GetCenter().x - g.x / 2), std::round(x.GetCenter().y - g.y / 2)),
                        u32(overX ? p.text : p.muted), icon::Close);
            ImGui::PopFont();
            if (overX) ImGui::SetTooltip("Clear");
        }

        dl->ChannelsSetCurrent(0);
        bool hovered = over && !overX && !e.url.empty();
        dl->AddRectFilled(start, end, u32(hovered ? p.hover : p.card));
        if (e.priority == Priority::High && !e.quiet)
            dl->AddRectFilled(start, ImVec2(start.x + 3 * scale_, end.y), u32(p.accent));
        if (hovered) ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
        if (ImGui::IsItemClicked() && !onBar) {
            if (overX) clear = e.id;
            else OpenURL(e.url);
        }
        dl->ChannelsMerge();
        ImGui::Dummy(ImVec2(0, 1 * scale_));
        ImGui::PopID();
    }
    if (clear != 0) {
        model_.Clear(clear);
        config_.cleared = model_.GetCleared();
        SaveConfig(config_);
    }
}

void App::openSettings() {
    snprintf(urlBuf_, sizeof(urlBuf_), "%s", config_.url.c_str());
    snprintf(tokenBuf_, sizeof(tokenBuf_), "%s", config_.token.c_str());
    priorityChoice_ = int(std::find(std::begin(kPriorities), std::end(kPriorities), config_.minPriority) - std::begin(kPriorities));
    notifyChoice_ = config_.notify;
    startChoice_ = StartsWithWindows();
    oledChoice_ = config_.oled;
    catChoice_ = config_.cat;
    settingsOpen_ = true;
}

void App::settings() {
    if (settingsOpen_ && !ImGui::IsPopupOpen("Settings")) ImGui::OpenPopup("Settings");
    ImGuiIO& io = ImGui::GetIO();
    ImGui::SetNextWindowPos(ImVec2(io.DisplaySize.x / 2, io.DisplaySize.y / 2), ImGuiCond_Always, ImVec2(0.5f, 0.5f));
    ImGui::SetNextWindowSize(ImVec2(std::min(520 * scale_, io.DisplaySize.x - 32 * scale_), 0));
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(24 * scale_, 20 * scale_));
    bool open = ImGui::BeginPopupModal("Settings", nullptr, ImGuiWindowFlags_NoTitleBar | ImGuiWindowFlags_NoMove |
                                                               ImGuiWindowFlags_NoResize | ImGuiWindowFlags_NoSavedSettings);
    ImGui::PopStyleVar();
    if (!open) return;
    ImGui::PushFont(fonts_.title);
    ImGui::TextUnformatted("Settings");
    ImGui::PopFont();
    ImGui::Dummy(ImVec2(0, 4 * scale_));

    float w = ImGui::GetContentRegionAvail().x;
    mutedText("Server address");
    ImGui::SetNextItemWidth(w);
    if (ImGui::IsWindowAppearing()) ImGui::SetKeyboardFocusHere();
    ImGui::InputTextWithHint("##url", "https://example.com/kitchensink", urlBuf_, sizeof(urlBuf_));
    mutedText("Token");
    ImGui::SetNextItemWidth(w);
    ImGui::InputText("##token", tokenBuf_, sizeof(tokenBuf_), ImGuiInputTextFlags_Password);
    ImGui::Dummy(ImVec2(0, 4 * scale_));

    ImGui::Checkbox("Show Windows notifications", &notifyChoice_);
    ImGui::BeginDisabled(!notifyChoice_);
    mutedText("Notify me about");
    ImGui::SetNextItemWidth(w);
    ImGui::Combo("##priority", &priorityChoice_, kPriorityNames, IM_ARRAYSIZE(kPriorityNames));
    ImGui::EndDisabled();
    ImGui::Checkbox("Start with Windows", &startChoice_);
    ImGui::Dummy(ImVec2(0, 4 * scale_));
    mutedText("OLED care, against burn-in");
    auto care = [&](const char* label, bool* on, const char* what) {
        ImGui::Checkbox(label, on);
        ImGui::SameLine();
        ImGui::PushFont(fonts_.caption);
        ImGui::AlignTextToFramePadding();
        mutedText(what);
        ImGui::PopFont();
    };
    care("Shift", &oledChoice_.shift, "moves everything a few pixels every three minutes");
    care("True black", &oledChoice_.black, "a black theme with dimmer text and borders");
    care("Dim when idle", &oledChoice_.dim, "after ten minutes without the mouse over the window");
    care("Swap sides", &oledChoice_.swap, "moves the Recent column across every other hour");
    ImGui::Dummy(ImVec2(0, 4 * scale_));
    ImGui::Checkbox("Cat", &catChoice_);
    ImGui::SameLine();
    ImGui::PushFont(fonts_.caption);
    ImGui::AlignTextToFramePadding();
    mutedText("a black cat asleep above the Recent column");
    ImGui::PopFont();
    ImGui::Dummy(ImVec2(0, 8 * scale_));

    bool configured = !config_.url.empty() && !config_.token.empty();
    const ImGuiStyle& st = ImGui::GetStyle();
    float bw = 96 * scale_;
    ImGui::SetCursorPosX(ImGui::GetCursorPosX() + w - (configured ? bw * 2 + st.ItemSpacing.x : bw));
    if (configured) {
        if (ImGui::Button("Cancel", ImVec2(bw, 0)) || ImGui::IsKeyPressed(ImGuiKey_Escape)) {
            settingsOpen_ = false;
            ImGui::CloseCurrentPopup();
        }
        ImGui::SameLine();
    }
    ImGui::PushStyleColor(ImGuiCol_Button, Colours().accent);
    ImGui::PushStyleColor(ImGuiCol_ButtonHovered, fade(Colours().accent, 0.85f));
    ImGui::PushStyleColor(ImGuiCol_Text, ImVec4(1, 1, 1, 1));
    if (ImGui::Button("Save", ImVec2(bw, 0)) || ImGui::IsKeyPressed(ImGuiKey_Enter)) {
        Config c = config_;
        c.url = urlBuf_;
        c.token = tokenBuf_;
        c.notify = notifyChoice_;
        c.oled = oledChoice_;
        c.cat = catChoice_;
        c.minPriority = kPriorities[std::clamp(priorityChoice_, 0, 2)];
        bool reconnect = c.url != config_.url || c.token != config_.token;
        config_ = c;
        SaveConfig(config_);
        SetStartWithWindows(startChoice_);
        ApplyTheme(dark_, scale_, compact_, config_.oled.black);
        if (reconnect) {
            model_ = Model();
            client_->Start(config_.url, config_.token);
        }
        settingsOpen_ = false;
        ImGui::CloseCurrentPopup();
    }
    ImGui::PopStyleColor(3);
    ImGui::EndPopup();
}

}  // namespace ks
