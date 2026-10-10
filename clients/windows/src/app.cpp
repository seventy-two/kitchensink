#include "app.h"

#include <windows.h>
#include <shellapi.h>

#include <algorithm>
#include <ctime>

#include "imgui.h"
#include "imgui_internal.h"

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

void wrapped(const std::string& s, const ImVec4& colour) {
    ImGui::PushStyleColor(ImGuiCol_Text, colour);
    ImGui::PushTextWrapPos(0);
    ImGui::TextUnformatted(s.c_str());
    ImGui::PopTextWrapPos();
    ImGui::PopStyleColor();
}

// sparkline draws a series as a filled line chart.
void sparkline(const std::vector<float>& v, ImVec2 size, const ImVec4& colour) {
    ImVec2 p = ImGui::GetCursorScreenPos();
    ImGui::Dummy(size);
    if (v.size() < 2) return;
    auto [lo, hi] = std::minmax_element(v.begin(), v.end());
    float min = std::min(*lo, 0.0f), range = std::max(*hi - min, 1e-6f);
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

App::App(ID3D11Device* device, std::function<void()> wake,
         std::function<void(const std::string&, const std::string&)> notify)
    : config_(LoadConfig()),
      client_(std::make_unique<Client>(wake)),
      images_(std::make_unique<ImageCache>(device, wake)),
      notify_(std::move(notify)) {
    client_->Start(config_.url, config_.token);
    if (config_.url.empty() || config_.token.empty()) openSettings();
}

App::~App() {
    client_.reset();
    images_.reset();
}

void App::SetScale(float scale) {
    scale_ = scale;
    ApplyTheme(dark_, scale_);
    fonts_ = LoadFonts(scale_);
}

void App::SetDark(bool dark) {
    dark_ = dark;
    ApplyTheme(dark_, scale_);
}

void App::Pump() {
    std::vector<Inbound> in;
    client_->Drain(in);
    for (auto& m : in) {
        switch (m.type) {
        case Inbound::Type::SyncStart: model_.BeginSync(); break;
        case Inbound::Type::Ready: model_.EndSync(); break;
        case Inbound::Type::Snapshot:
            refreshing_.erase(m.snapshot.monitor);
            model_.ApplySnapshot(std::move(m.snapshot));
            break;
        case Inbound::Type::Event: {
            Event e = m.event;
            if (!model_.AddEvent(std::move(m.event))) break;
            // History from before the app started has already been seen
            // (or missed for good); events missed while offline are new.
            if (!m.history && !e.quiet && config_.notify && e.priority >= config_.minPriority)
                pending_.push_back(std::move(e));
            break;
        }
        }
    }
    images_->Upload();

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
    ImGuiIO& io = ImGui::GetIO();
    ImGui::SetNextWindowPos(ImVec2(0, 0));
    ImGui::SetNextWindowSize(io.DisplaySize);
    ImGui::Begin("##root", nullptr,
                 ImGuiWindowFlags_NoDecoration | ImGuiWindowFlags_NoMove | ImGuiWindowFlags_NoSavedSettings |
                     ImGuiWindowFlags_NoBringToFrontOnFocus | ImGuiWindowFlags_NoScrollbar |
                     ImGuiWindowFlags_NoScrollWithMouse);

    header();
    ImGui::Dummy(ImVec2(0, 4 * scale_));

    float gap = 16 * scale_;
    float avail = ImGui::GetContentRegionAvail().x;
    float feedW = avail >= 980 * scale_ ? 340 * scale_ : 0;
    float cardsW = feedW > 0 ? avail - feedW - gap : avail;

    ConnState state = client_->State();
    if (state == ConnState::NotConfigured) {
        empty("Add your server's address and token to get started.", true);
    } else if (model_.Snapshots().empty()) {
        empty(state == ConnState::Offline ? "Can't reach the server." : "Connecting…", state == ConnState::Offline);
    } else {
        ImGui::BeginChild("##cards", ImVec2(cardsW, 0), ImGuiChildFlags_None, ImGuiWindowFlags_NoBackground);
        cards(ImGui::GetContentRegionAvail().x);
        ImGui::EndChild();
        if (feedW > 0) {
            ImGui::SameLine(0, gap);
            ImGui::BeginChild("##feed", ImVec2(feedW, 0), ImGuiChildFlags_None, ImGuiWindowFlags_NoBackground);
            feed();
            ImGui::EndChild();
        }
    }

    settings();
    busy_ = ImGui::IsAnyItemActive() || ImGui::GetIO().WantTextInput || settingsOpen_;
    ImGui::End();
}

void App::header() {
    const Palette& p = Colours();
    float top = ImGui::GetCursorPosY();
    ImGui::PushFont(fonts_.title);
    ImGui::TextUnformatted("kitchensink");
    float titleH = ImGui::GetItemRectSize().y;
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
    ImGui::SetCursorPosY(top + (titleH - textH) / 2);
    ImVec2 pos = ImGui::GetCursorScreenPos();
    float r = 4 * scale_;
    ImGui::GetWindowDrawList()->AddCircleFilled(ImVec2(pos.x + r, pos.y + textH / 2), r, u32(dot));
    ImGui::Dummy(ImVec2(r * 2, textH));
    ImGui::SameLine(0, 6 * scale_);
    ImGui::SetCursorPosY(top + (titleH - textH) / 2);
    mutedText(status);
    ImGui::PopFont();

    float bw = ImGui::CalcTextSize(icon::Settings).x + ImGui::GetStyle().FramePadding.x * 2;
    ImGui::SameLine(ImGui::GetWindowContentRegionMax().x - bw);
    ImGui::SetCursorPosY(top + (titleH - ImGui::GetFrameHeight()) / 2);
    if (ImGui::Button(icon::Settings)) openSettings();
    if (ImGui::IsItemHovered()) ImGui::SetTooltip("Settings");
    ImGui::SetCursorPosY(top + std::max(titleH, ImGui::GetFrameHeight()) + ImGui::GetStyle().ItemSpacing.y);
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
    const auto& snaps = model_.Snapshots();
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
}

void App::card(const Snapshot& s, float width) {
    const Palette& p = Colours();
    ImGui::PushStyleColor(ImGuiCol_ChildBg, p.card);
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(18 * scale_, 14 * scale_));
    ImGui::BeginChild(s.monitor.c_str(), ImVec2(width, 0),
                      ImGuiChildFlags_Borders | ImGuiChildFlags_AutoResizeY | ImGuiChildFlags_AlwaysUseWindowPadding,
                      ImGuiWindowFlags_NoScrollbar | ImGuiWindowFlags_NoScrollWithMouse);
    ImGui::PopStyleVar();

    // Title on the left; when it was updated and a refresh button on the
    // right.
    ImGui::PushFont(fonts_.heading);
    ImGui::AlignTextToFramePadding();
    ImGui::TextUnformatted(s.title.c_str());
    ImGui::PopFont();

    bool pending = refreshing_.count(s.monitor) && ImGui::GetTime() - refreshing_[s.monitor] < 15;
    std::string when = pending ? "refreshing…" : "updated " + Ago(s.updated, now());
    ImGui::PushFont(fonts_.caption);
    ImGuiStyle& st = ImGui::GetStyle();
    float bw = ImGui::CalcTextSize(icon::Refresh).x + st.FramePadding.x * 2;
    float ww = ImGui::CalcTextSize(when.c_str()).x;
    ImGui::SameLine(ImGui::GetWindowContentRegionMax().x - bw - ww - st.ItemSpacing.x);
    ImGui::AlignTextToFramePadding();
    mutedText(when);
    ImGui::SameLine();
    ImGui::PushStyleColor(ImGuiCol_Button, ImVec4(0, 0, 0, 0));
    ImGui::PushID(s.monitor.c_str());
    ImGui::BeginDisabled(pending);
    if (ImGui::Button(icon::Refresh)) {
        refreshing_[s.monitor] = ImGui::GetTime();
        client_->Refresh(s.monitor);
    }
    ImGui::EndDisabled();
    ImGui::PopID();
    ImGui::PopStyleColor();
    ImGui::PopFont();

    if (!s.error.empty()) {
        ImGui::PushFont(fonts_.caption);
        wrapped(std::string(icon::Warning) + "  " + s.error, p.warn);
        ImGui::PopFont();
    }
    if (s.blocks.empty() && s.error.empty()) mutedText("Waiting for the first check…");

    for (size_t i = 0; i < s.blocks.size(); i++) {
        ImGui::PushID(int(i));
        ImGui::Dummy(ImVec2(0, 2 * scale_));
        block(s.blocks[i]);
        ImGui::PopID();
    }

    ImGui::EndChild();
    ImGui::PopStyleColor();
    heights_[s.monitor] = ImGui::GetItemRectSize().y;
}

void App::block(const Block& b) {
    if (!b.title.empty() && b.type != "stats") {
        ImGui::PushFont(fonts_.caption);
        mutedText(b.title);
        ImGui::PopFont();
    }
    if (b.type == "stats") stats(b);
    else if (b.type == "table") table(b);
    else if (b.type == "list") list(b);
    else if (!b.text.empty()) wrapped(b.text, Colours().text);
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
        // A flat series, e.g. no rain at all, says nothing worth a chart.
        bool flat = s.series.empty() ||
                    *std::max_element(s.series.begin(), s.series.end()) == *std::min_element(s.series.begin(), s.series.end());
        if (!flat) {
            sparkline(s.series, ImVec2(tileW, 22 * scale_), s.tone == Tone::None ? Colours().accent : ToneColour(s.tone));
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
    // The left-aligned column with the most text takes the spare width;
    // the rest fit their contents.
    int stretch = -1;
    size_t most = 0;
    for (size_t c = 0; c < cols; c++) {
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
            float r = 6 * scale_;
            if (const Texture* t = images_->Get(it.image)) {
                // Crop to the thumbnail's shape rather than squash.
                float ta = float(t->width) / float(t->height), ba = thumbW / thumbH;
                ImVec2 uv0(0, 0), uv1(1, 1);
                if (ta > ba) uv0.x = (1 - ba / ta) / 2, uv1.x = 1 - uv0.x;
                else uv0.y = (1 - ta / ba) / 2, uv1.y = 1 - uv0.y;
                dl->AddImageRounded((ImTextureID)(intptr_t)t->srv, tp, tq, uv0, uv1, IM_COL32_WHITE, r);
            } else {
                dl->AddRectFilled(tp, tq, u32(p.frame), r);
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
            dl->AddRectFilled(start, end, u32(p.hover), 8 * scale_);
            ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
        }
        if (ImGui::IsItemClicked()) OpenURL(it.url);
        dl->ChannelsMerge();
        ImGui::PopID();
    }
}

void App::feed() {
    const Palette& p = Colours();
    ImGui::PushFont(fonts_.heading);
    ImGui::TextUnformatted("Recent");
    ImGui::PopFont();
    ImGui::Dummy(ImVec2(0, 2 * scale_));
    const auto& events = model_.Events();
    if (events.empty()) {
        mutedText("Nothing yet.");
        return;
    }
    ImDrawList* dl = ImGui::GetWindowDrawList();
    float pad = 10 * scale_;
    int64_t t = now();
    size_t shown = 0;
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

        dl->ChannelsSetCurrent(0);
        bool hovered = ImGui::IsItemHovered() && !e.url.empty();
        dl->AddRectFilled(start, end, u32(hovered ? p.hover : p.card), 8 * scale_);
        if (e.priority == Priority::High && !e.quiet)
            dl->AddRectFilled(start, ImVec2(start.x + 3 * scale_, end.y), u32(p.accent), 8 * scale_,
                              ImDrawFlags_RoundCornersLeft);
        if (hovered) ImGui::SetMouseCursor(ImGuiMouseCursor_Hand);
        if (ImGui::IsItemClicked()) OpenURL(e.url);
        dl->ChannelsMerge();
        ImGui::Dummy(ImVec2(0, 1 * scale_));
        ImGui::PopID();
    }
}

void App::openSettings() {
    snprintf(urlBuf_, sizeof(urlBuf_), "%s", config_.url.c_str());
    snprintf(tokenBuf_, sizeof(tokenBuf_), "%s", config_.token.c_str());
    priorityChoice_ = int(std::find(std::begin(kPriorities), std::end(kPriorities), config_.minPriority) - std::begin(kPriorities));
    notifyChoice_ = config_.notify;
    startChoice_ = StartsWithWindows();
    settingsOpen_ = true;
}

void App::settings() {
    if (settingsOpen_ && !ImGui::IsPopupOpen("Settings")) ImGui::OpenPopup("Settings");
    ImGuiIO& io = ImGui::GetIO();
    ImGui::SetNextWindowPos(ImVec2(io.DisplaySize.x / 2, io.DisplaySize.y / 2), ImGuiCond_Always, ImVec2(0.5f, 0.5f));
    ImGui::SetNextWindowSize(ImVec2(std::min(520 * scale_, io.DisplaySize.x - 32 * scale_), 0));
    // Modals take window rounding, which the root window keeps at 0.
    ImGui::PushStyleVar(ImGuiStyleVar_WindowPadding, ImVec2(24 * scale_, 20 * scale_));
    ImGui::PushStyleVar(ImGuiStyleVar_WindowRounding, 12 * scale_);
    bool open = ImGui::BeginPopupModal("Settings", nullptr, ImGuiWindowFlags_NoTitleBar | ImGuiWindowFlags_NoMove |
                                                               ImGuiWindowFlags_NoResize | ImGuiWindowFlags_NoSavedSettings);
    ImGui::PopStyleVar(2);
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
        c.minPriority = kPriorities[std::clamp(priorityChoice_, 0, 2)];
        bool reconnect = c.url != config_.url || c.token != config_.token;
        config_ = c;
        SaveConfig(config_);
        SetStartWithWindows(startChoice_);
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
