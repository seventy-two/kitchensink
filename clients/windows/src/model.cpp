#include "model.h"

#include <algorithm>
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

Block parseBlock(const json& j) {
    Block b;
    b.type = str(j, "type");
    b.title = str(j, "title");
    b.text = str(j, "text");
    for (const auto& s : array(j, "stats")) {
        Stat st{str(s, "label"), str(s, "value"), str(s, "detail"), tone(s), {}};
        for (const auto& v : array(s, "series"))
            if (v.is_number()) st.series.push_back(v.get<float>());
        b.stats.push_back(std::move(st));
    }
    for (const auto& c : array(j, "columns")) {
        std::string a = str(c, "align");
        b.columns.push_back({str(c, "title"), a == "right" ? Align::Right : a == "center" ? Align::Center : Align::Left});
    }
    for (const auto& r : array(j, "rows")) {
        std::vector<Cell> row;
        for (const auto& c : r) row.push_back({str(c, "text"), str(c, "url"), tone(c)});
        b.rows.push_back(std::move(row));
    }
    for (const auto& i : array(j, "items"))
        b.items.push_back({str(i, "title"), str(i, "subtitle"), str(i, "detail"), str(i, "url"), str(i, "image"), tone(i)});
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
    // Go sends its zero time, year 1, for a monitor that hasn't polled yet.
    s.updated = std::max<int64_t>(0, ParseTime(str(j, "updated")));
    for (const auto& b : array(j, "blocks")) s.blocks.push_back(parseBlock(b));
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
    // Keep newest first; events nearly always arrive in order.
    auto it = events_.begin();
    while (it != events_.end() && it->id > e.id) ++it;
    events_.insert(it, std::move(e));
    while (events_.size() > kMaxEvents) events_.pop_back();
    return true;
}

std::string Model::TitleOf(const std::string& monitor) const {
    for (const auto& s : snapshots_)
        if (s.monitor == monitor && !s.title.empty()) return s.title;
    return monitor;
}

}  // namespace ks
