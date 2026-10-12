#include "model.h"

#include <algorithm>
#include <cmath>
#include <cstdio>

namespace ks {

namespace {

using nlohmann::json;

// str reads a string field, tolerating it being missing or another type.
std::string str(const json& j, const char* key) {
    auto it = j.find(key);
    if (it == j.end() || !it->is_string()) return {};
    return it->get<std::string>();
}

Tone tone(const json& j) {
    std::string t = str(j, "tone");
    if (t == "good") return Tone::Good;
    if (t == "bad") return Tone::Bad;
    if (t == "warn") return Tone::Warn;
    if (t == "muted") return Tone::Muted;
    if (t == "accent") return Tone::Accent;
    return Tone::None;
}

const json& array(const json& j, const char* key) {
    static const json empty = json::array();
    auto it = j.find(key);
    if (it == j.end() || !it->is_array()) return empty;
    return *it;
}

bool flag(const json& j, const char* key) {
    auto it = j.find(key);
    return it != j.end() && it->is_boolean() && it->get<bool>();
}

Stat parseStat(const json& j) {
    Stat st{str(j, "label"), str(j, "value"), str(j, "detail"), tone(j), {}};
    for (const auto& v : array(j, "series"))
        if (v.is_number()) st.series.push_back(v.get<float>());
    st.isPrivate = flag(j, "private");
    st.icon = str(j, "icon");
    return st;
}

Block parseBlock(const json& j) {
    Block b;
    b.type = str(j, "type");
    b.title = str(j, "title");
    b.text = str(j, "text");
    for (const auto& s : array(j, "stats")) b.stats.push_back(parseStat(s));
    for (const auto& c : array(j, "columns")) {
        std::string a = str(c, "align");
        auto fill = c.find("fill");
        b.columns.push_back({str(c, "title"), a == "right" ? Align::Right : a == "center" ? Align::Center : Align::Left,
                             fill != c.end() && fill->is_boolean() && fill->get<bool>()});
    }
    for (const auto& r : array(j, "rows")) {
        std::vector<Cell> row;
        for (const auto& c : r) row.push_back({str(c, "text"), str(c, "url"), tone(c), str(c, "detail")});
        b.rows.push_back(std::move(row));
    }
    for (const auto& i : array(j, "items")) {
        auto done = i.find("done");
        b.items.push_back({str(i, "title"), str(i, "subtitle"), str(i, "detail"), str(i, "url"), str(i, "image"),
                           tone(i), str(i, "id"), done != i.end() && done->is_boolean() && done->get<bool>()});
    }
    b.addHint = str(j, "addHint");
    b.isPrivate = flag(j, "private");
    return b;
}

// daysFromCivil counts days from 1970-01-01 to a date in the proleptic
// Gregorian calendar (Howard Hinnant's algorithm).
int64_t daysFromCivil(int64_t y, unsigned m, unsigned d) {
    y -= m <= 2;
    const int64_t era = (y >= 0 ? y : y - 399) / 400;
    const unsigned yoe = static_cast<unsigned>(y - era * 400);
    const unsigned doy = (153 * (m + (m > 2 ? -3 : 9)) + 2) / 5 + d - 1;
    const unsigned doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    return era * 146097 + static_cast<int64_t>(doe) - 719468;
}

}  // namespace

Priority ParsePriority(const std::string& s) {
    if (s == "low") return Priority::Low;
    if (s == "high") return Priority::High;
    return Priority::Normal;
}

Snapshot ParseSnapshot(const json& j) {
    Snapshot s;
    s.monitor = str(j, "monitor");
    s.kind = str(j, "kind");
    s.title = str(j, "title");
    s.error = str(j, "error");
    s.subtitle = str(j, "subtitle");
    s.timely = j.value("timely", false);
    // Go sends its zero time, year 1, for a monitor that hasn't polled yet.
    s.updated = std::max<int64_t>(0, ParseTime(str(j, "updated")));
    for (const auto& b : array(j, "blocks")) s.blocks.push_back(parseBlock(b));
    for (const auto& st : array(j, "summary")) s.summary.push_back(parseStat(st));
    s.summaryStyle = str(j, "summaryStyle");
    return s;
}

Fit FitPanels(const std::vector<Panel>& panels, float height, float gap, float minBody) {
    Fit fit;
    fit.show.assign(panels.size(), Show::Row);
    float total = 0;
    for (size_t i = 0; i < panels.size(); i++) {
        const Panel& p = panels[i];
        bool open = p.pin == Pin::Open && p.full > 0;
        if (open) fit.show[i] = Show::Full;
        total += (open ? p.full : p.row) + gap;
    }
    for (size_t i = 0; i < panels.size(); i++) {
        const Panel& p = panels[i];
        if (p.pin != Pin::Auto || p.full <= 0 || total - p.row + p.full > height) continue;
        fit.show[i] = Show::Full;
        total += p.full - p.row;
    }
    for (size_t i = 0; i < panels.size(); i++) {
        const Panel& p = panels[i];
        if (p.pin != Pin::Auto || fit.show[i] != Show::Row || p.full <= 0) continue;
        float body = height - total - p.overhead;
        if (body >= minBody) {
            fit.show[i] = Show::Clipped;
            fit.body = body;
        }
        break;
    }
    return fit;
}

std::vector<std::string> Arrange(const std::vector<std::string>& defaults, const std::vector<std::string>& saved) {
    auto has = [](const std::vector<std::string>& v, const std::string& id) {
        return std::find(v.begin(), v.end(), id) != v.end();
    };
    std::vector<std::string> out;
    for (const auto& id : saved)
        if (has(defaults, id) && !has(out, id)) out.push_back(id);
    for (size_t i = 0; i < defaults.size(); i++) {
        if (has(out, defaults[i])) continue;
        // After the panel it follows by default, or first if none.
        auto pos = out.begin();
        if (i > 0) {
            auto prev = std::find(out.begin(), out.end(), defaults[i - 1]);
            if (prev != out.end()) pos = prev + 1;
        }
        out.insert(pos, defaults[i]);
    }
    return out;
}

std::vector<std::string> Move(std::vector<std::string> order, const std::string& id, const std::string& target,
                              bool after) {
    if (id == target) return order;
    auto from = std::find(order.begin(), order.end(), id);
    if (from == order.end() || std::find(order.begin(), order.end(), target) == order.end()) return order;
    order.erase(from);
    auto to = std::find(order.begin(), order.end(), target);
    order.insert(after ? to + 1 : to, id);
    return order;
}

std::pair<float, float> OledShift(int64_t t, float r) {
    constexpr int64_t kStep = 3 * 60;
    constexpr double kPi = 3.14159265358979323846;
    double angle = double((t / kStep) % 8) * kPi / 4;
    return {float(std::round(std::cos(angle) * r)), float(std::round(std::sin(angle) * r))};
}

bool OledSwapSides(int64_t t) { return (t / 3600) % 2 == 1; }

bool OledDim(int64_t now, int64_t lastActivity, int64_t wakeUntil) {
    constexpr int64_t kIdle = 10 * 60;
    return now - lastActivity >= kIdle && now >= wakeUntil;
}

bool UseCompact(bool compact, float width, float height, float scale) {
    // Below these, at 100% scale, the full panels don't fit at a glance.
    constexpr float kHeight = 1100, kWidth = 900, kSlack = 40;
    float slack = compact ? kSlack : -kSlack;
    return height < (kHeight + slack) * scale || width < (kWidth + slack) * scale;
}

bool HasPrivate(const Snapshot& s) {
    auto stat = [](const Stat& st) { return st.isPrivate; };
    return std::any_of(s.summary.begin(), s.summary.end(), stat) ||
           std::any_of(s.blocks.begin(), s.blocks.end(), [&](const Block& b) {
               return b.isPrivate || std::any_of(b.stats.begin(), b.stats.end(), stat);
           });
}

Snapshot WithoutPrivate(Snapshot s) {
    auto stat = [](const Stat& st) { return st.isPrivate; };
    s.summary.erase(std::remove_if(s.summary.begin(), s.summary.end(), stat), s.summary.end());
    for (auto& b : s.blocks) b.stats.erase(std::remove_if(b.stats.begin(), b.stats.end(), stat), b.stats.end());
    // A stats block left with nothing goes too.
    s.blocks.erase(std::remove_if(s.blocks.begin(), s.blocks.end(),
                                  [](const Block& b) { return b.isPrivate || (b.type == "stats" && b.stats.empty()); }),
                   s.blocks.end());
    return s;
}

Event ParseEvent(const json& j) {
    Event e;
    if (auto it = j.find("id"); it != j.end() && it->is_number_integer()) e.id = it->get<int64_t>();
    e.monitor = str(j, "monitor");
    e.key = str(j, "key");
    e.title = str(j, "title");
    e.body = str(j, "body");
    e.url = str(j, "url");
    e.image = str(j, "image");
    e.time = ParseTime(str(j, "time"));
    e.priority = ParsePriority(str(j, "priority"));
    if (auto it = j.find("quiet"); it != j.end() && it->is_boolean()) e.quiet = it->get<bool>();
    return e;
}

int64_t ParseTime(const std::string& s) {
    int y, mo, d, h, mi, sec;
    int n = 0;
    if (std::sscanf(s.c_str(), "%4d-%2d-%2dT%2d:%2d:%2d%n", &y, &mo, &d, &h, &mi, &sec, &n) != 6 || n != 19) return 0;
    if (mo < 1 || mo > 12 || d < 1 || d > 31) return 0;
    size_t i = 19;
    if (i < s.size() && s[i] == '.') {
        ++i;
        while (i < s.size() && s[i] >= '0' && s[i] <= '9') ++i;
    }
    int64_t offset = 0;
    if (i < s.size() && (s[i] == '+' || s[i] == '-')) {
        int oh, om;
        if (std::sscanf(s.c_str() + i + 1, "%2d:%2d", &oh, &om) != 2) return 0;
        offset = (oh * 60 + om) * 60 * (s[i] == '-' ? -1 : 1);
    } else if (i >= s.size() || (s[i] != 'Z' && s[i] != 'z')) {
        return 0;
    }
    return daysFromCivil(y, mo, d) * 86400 + h * 3600 + mi * 60 + sec - offset;
}

std::string Ago(int64_t then, int64_t now) {
    int64_t s = now - then;
    if (then == 0) return "never";
    if (s < 60) return "just now";
    if (s < 3600) return std::to_string(s / 60) + "m ago";
    if (s < 86400) return std::to_string(s / 3600) + "h ago";
    return std::to_string(s / 86400) + "d ago";
}

void Model::BeginSync() {
    syncing_ = true;
    synced_.clear();
}

void Model::EndSync() {
    if (!syncing_) return;
    syncing_ = false;
    snapshots_.erase(std::remove_if(snapshots_.begin(), snapshots_.end(),
                                    [&](const Snapshot& s) { return !synced_.count(s.monitor); }),
                     snapshots_.end());
}

void Model::ApplySnapshot(Snapshot s) {
    if (syncing_) {
        // The server sends snapshots in config order; keep that order.
        size_t pos = synced_.size();
        synced_.insert(s.monitor);
        auto it = std::find_if(snapshots_.begin(), snapshots_.end(),
                               [&](const Snapshot& x) { return x.monitor == s.monitor; });
        if (it != snapshots_.end()) snapshots_.erase(it);
        snapshots_.insert(snapshots_.begin() + std::min(pos, snapshots_.size()), std::move(s));
        return;
    }
    for (auto& x : snapshots_) {
        if (x.monitor == s.monitor) {
            x = std::move(s);
            return;
        }
    }
    snapshots_.push_back(std::move(s));
}

bool Model::AddEvent(Event e) {
    for (const auto& x : events_)
        if (x.id == e.id) return false;
    lastEventID_ = std::max(lastEventID_, e.id);
    if (e.id <= cleared_.through || cleared_.ids.count(e.id)) return false;
    // Keep newest first; events nearly always arrive in order.
    auto it = events_.begin();
    while (it != events_.end() && it->id > e.id) ++it;
    events_.insert(it, std::move(e));
    while (events_.size() > kMaxEvents) events_.pop_back();
    return true;
}

void Model::Clear(int64_t id) {
    events_.erase(std::remove_if(events_.begin(), events_.end(), [&](const Event& e) { return e.id == id; }), events_.end());
    if (id <= cleared_.through) return;
    cleared_.ids.insert(id);
    while (cleared_.ids.size() > kMaxCleared) cleared_.ids.erase(cleared_.ids.begin());
}

void Model::ClearAll() {
    events_.clear();
    cleared_.through = std::max(cleared_.through, lastEventID_);
    cleared_.ids.erase(cleared_.ids.begin(), cleared_.ids.upper_bound(cleared_.through));
}

void Model::SetCleared(Cleared c) {
    cleared_ = std::move(c);
    cleared_.ids.erase(cleared_.ids.begin(), cleared_.ids.upper_bound(cleared_.through));
    while (cleared_.ids.size() > kMaxCleared) cleared_.ids.erase(cleared_.ids.begin());
    events_.erase(std::remove_if(events_.begin(), events_.end(),
                                 [&](const Event& e) { return e.id <= cleared_.through || cleared_.ids.count(e.id); }),
                  events_.end());
}

std::string Model::TitleOf(const std::string& monitor) const {
    for (const auto& s : snapshots_)
        if (s.monitor == monitor && !s.title.empty()) return s.title;
    return monitor;
}

}  // namespace ks
