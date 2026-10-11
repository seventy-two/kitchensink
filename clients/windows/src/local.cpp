#include "local.h"

#include <algorithm>
#include <cctype>
#include <cstdio>
#include <map>

namespace ks {

namespace {

std::string percent(float v) {
    char buf[16];
    std::snprintf(buf, sizeof(buf), "%.0f%%", v);
    return buf;
}

float share(uint64_t part, uint64_t whole) { return whole ? 100.0f * float(part) / float(whole) : -1; }

// stat makes a tile for a percentage, with its history as a sparkline.
Stat stat(const std::string& label, float pct, const std::string& detail, std::vector<float> series) {
    if (pct < 0) return Stat{label, "–", detail, Tone::Muted, {}};
    return Stat{label, percent(pct), detail, LoadTone(pct), std::move(series), 100};
}

std::string driveName(const std::string& root) {
    std::string name = root;
    while (!name.empty() && (name.back() == '\\' || name.back() == '/')) name.pop_back();
    return name;
}

}  // namespace

Tone LoadTone(float pct) {
    if (pct >= 95) return Tone::Bad;
    if (pct >= 85) return Tone::Warn;
    return Tone::None;
}

std::string FormatBytes(uint64_t bytes) {
    constexpr double kGB = 1024.0 * 1024 * 1024;
    char buf[32];
    double gb = double(bytes) / kGB;
    if (gb >= 1024) std::snprintf(buf, sizeof(buf), "%.1f TB", gb / 1024);
    else if (gb >= 1) std::snprintf(buf, sizeof(buf), "%.1f GB", gb);
    else std::snprintf(buf, sizeof(buf), "%.0f MB", double(bytes) / (1024 * 1024));
    return buf;
}

float GpuUtilisation(const std::vector<std::pair<std::string, double>>& instances) {
    std::map<std::string, double> engines;
    for (const auto& [name, value] : instances) {
        // The engine is everything from the adapter to the engine number;
        // the process before it and the engine type after don't matter.
        size_t from = name.find("luid_"), to = name.find("_engtype_");
        if (from == std::string::npos || to == std::string::npos || to < from) continue;
        engines[name.substr(from, to - from)] += value;
    }
    double busiest = 0;
    for (const auto& [engine, total] : engines) busiest = std::max(busiest, total);
    return float(std::clamp(busiest, 0.0, 100.0));
}

std::string AppName(const std::string& appID) {
    std::string lower = appID;
    std::transform(lower.begin(), lower.end(), lower.begin(), [](unsigned char c) { return char(std::tolower(c)); });
    // Windows' own apps report package IDs; browsers and others their exe.
    static const std::pair<const char*, const char*> known[] = {
        {"spotify", "Spotify"},     {"msedge", "Edge"},          {"chrome", "Chrome"},
        {"firefox", "Firefox"},     {"308046b0af4a39cb", "Firefox"}, {"zunemusic", "Media Player"},
        {"zunevideo", "Films & TV"}, {"vlc", "VLC"},             {"foobar2000", "foobar2000"},
        {"itunes", "iTunes"},       {"applemusic", "Apple Music"}, {"brave", "Brave"},
        {"opera", "Opera"},         {"discord", "Discord"},
    };
    for (const auto& [needle, name] : known)
        if (lower.find(needle) != std::string::npos) return name;
    std::string name = appID.substr(0, appID.find('!'));
    if (name.size() > 4 && lower.compare(name.size() - 4, 4, ".exe") == 0) name.resize(name.size() - 4);
    return name;
}

std::string StripEmoji(const std::string& s) {
    std::string out;
    for (size_t i = 0; i < s.size();) {
        // Decode one UTF-8 character.
        unsigned char c = s[i];
        size_t n = c < 0x80 ? 1 : (c >> 5) == 6 ? 2 : (c >> 4) == 14 ? 3 : (c >> 3) == 30 ? 4 : 1;
        if (i + n > s.size()) n = 1;
        uint32_t cp = n == 1 ? c : c & (0x7F >> n);
        for (size_t k = 1; k < n; k++) cp = (cp << 6) | (s[i + k] & 0x3F);
        bool emoji = (cp >= 0x1F000 && cp <= 0x1FAFF) || (cp >= 0x2600 && cp <= 0x27BF) || cp == 0xFE0F ||
                     cp == 0x200D || (cp >= 0x1F1E6 && cp <= 0x1F1FF) || (cp >= 0xE0020 && cp <= 0xE007F);
        if (!emoji) out.append(s, i, n);
        i += n;
    }
    // Collapse the runs of spaces that removing them can leave.
    std::string tidy;
    for (char c : out)
        if (!(c == ' ' && (tidy.empty() || tidy.back() == ' '))) tidy += c;
    while (!tidy.empty() && tidy.back() == ' ') tidy.pop_back();
    return tidy;
}

std::string FormatTime(double seconds) {
    int s = int(std::max(seconds, 0.0) + 0.5);
    char buf[16];
    if (s >= 3600) std::snprintf(buf, sizeof(buf), "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60);
    else std::snprintf(buf, sizeof(buf), "%d:%02d", s / 60, s % 60);
    return buf;
}

Snapshot MediaSnapshot(const NowPlaying& np, int64_t now) {
    Snapshot snap;
    snap.monitor = "local:media";
    snap.kind = kLocalKind;
    snap.title = "Now playing";
    snap.updated = now;
    if (!np.active) return snap;

    std::string from;
    for (const std::string& part : {np.album, np.app})
        if (!part.empty()) from += (from.empty() ? "" : " · ") + part;
    Block b;
    b.type = "media";
    std::string title = StripEmoji(np.title), artist = StripEmoji(np.artist);
    from = StripEmoji(from);
    b.items.push_back({title.empty() ? "Unknown track" : title, artist, from, "", np.image});
    b.media = np.media;
    snap.blocks.push_back(b);
    std::string detail = artist;
    if (!np.app.empty()) detail += (detail.empty() ? "" : " · ") + np.app;
    snap.summary.push_back({np.media.playing ? "Now playing" : "Paused", b.items[0].title, detail,
                            np.media.playing ? Tone::None : Tone::Muted, {}});
    return snap;
}

void PcHistory::Add(PcSample s) {
    samples_.push_back(std::move(s));
    while (samples_.size() > kLength) samples_.pop_front();
}

Snapshot PcHistory::Build(int64_t now) const {
    Snapshot snap;
    snap.monitor = "local:pc";
    snap.kind = kLocalKind;
    snap.title = "This PC";
    snap.updated = now;
    if (samples_.empty()) return snap;

    std::vector<float> cpu, ram, gpu, vram;
    for (const auto& s : samples_) {
        cpu.push_back(std::max(s.cpu, 0.0f));
        ram.push_back(std::max(share(s.ramUsed, s.ramTotal), 0.0f));
        gpu.push_back(std::max(s.gpu, 0.0f));
        vram.push_back(std::max(share(s.vramUsed, s.vramTotal), 0.0f));
    }
    const PcSample& s = samples_.back();
    auto of = [](uint64_t used, uint64_t total) {
        return total ? FormatBytes(used) + " / " + FormatBytes(total) : std::string();
    };
    Block b;
    b.type = "stats";
    b.stats = {
        stat("CPU", s.cpu, "", cpu),
        stat("Memory", share(s.ramUsed, s.ramTotal), of(s.ramUsed, s.ramTotal), ram),
        stat("GPU", s.gpu, s.gpuName, gpu),
        stat("Video memory", share(s.vramUsed, s.vramTotal), of(s.vramUsed, s.vramTotal), vram),
    };
    snap.blocks.push_back(b);
    // The summary is the same tiles; it doesn't draw sparklines.
    snap.summary = b.stats;
    return snap;
}

Snapshot DrivesSnapshot(const std::vector<DriveInfo>& drives, int64_t now) {
    Snapshot snap;
    snap.monitor = "local:drives";
    snap.kind = kLocalKind;
    snap.title = "Drives";
    snap.updated = now;

    Block b;
    b.type = "table";
    b.columns = {{""}, {"Name", Align::Left, true}, {"Used", Align::Right}, {"Free", Align::Right},
                 {"Size", Align::Right}};
    for (const auto& d : drives) {
        if (d.total == 0) continue;
        float used = share(d.total - std::min(d.free, d.total), d.total);
        std::string name = driveName(d.root);
        b.rows.push_back({
            {name, "", Tone::None},
            {d.label.empty() ? "Local Disk" : d.label, "", Tone::Muted},
            {percent(used), "", LoadTone(used)},
            {FormatBytes(d.free), "", Tone::None},
            {FormatBytes(d.total), "", Tone::Muted},
        });
        snap.summary.push_back({name, percent(used), FormatBytes(d.free) + " free", LoadTone(used), {}});
    }
    if (b.rows.empty()) {
        b = Block{};
        b.type = "text";
        b.text = "No local drives found.";
    }
    snap.blocks.push_back(b);
    return snap;
}

}  // namespace ks
