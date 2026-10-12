// Tests for the dashboard's portable code. Build and run with build.bat, or
// on Linux: g++ -std=c++17 -I src -I third_party tests/tests.cpp src/model.cpp src/sse.cpp src/local.cpp src/cat.cpp
#include <cstdio>
#include <set>
#include <string>

#include <cmath>

#include "../src/cat.h"
#include "../src/local.h"
#include "../src/model.h"
#include "../src/sse.h"
#include "../src/weather_icon.h"

static int failures = 0;

#define CHECK(cond)                                                    \
    do {                                                               \
        if (!(cond)) {                                                 \
            std::printf("%s:%d: CHECK failed: %s\n", __FILE__, __LINE__, #cond); \
            ++failures;                                                \
        }                                                              \
    } while (0)

#define CHECK_EQ(a, b)                                                 \
    do {                                                               \
        auto va = (a);                                                 \
        auto vb = (b);                                                 \
        if (!(va == vb)) {                                             \
            std::printf("%s:%d: CHECK_EQ failed: %s != %s\n", __FILE__, __LINE__, #a, #b); \
            ++failures;                                                \
        }                                                              \
    } while (0)

using namespace ks;

static void testParseTime() {
    CHECK_EQ(ParseTime("1970-01-01T00:00:00Z"), int64_t(0));
    CHECK_EQ(ParseTime("2026-10-10T18:07:31.499738752Z"), int64_t(1791655651));
    CHECK_EQ(ParseTime("2026-10-10T19:07:31+01:00"), int64_t(1791655651));
    CHECK_EQ(ParseTime("2026-10-10T13:07:31.5-05:00"), int64_t(1791655651));
    CHECK_EQ(ParseTime("2000-02-29T00:00:00Z"), int64_t(951782400));
    CHECK_EQ(ParseTime("0001-01-01T00:00:00Z"), int64_t(-62135596800));  // Go's zero time
    CHECK_EQ(ParseTime(""), int64_t(0));
    CHECK_EQ(ParseTime("2026-10-10 18:07:31Z"), int64_t(0));
    CHECK_EQ(ParseTime("2026-10-10T18:07:31"), int64_t(0));  // no zone
}

static void testAgo() {
    CHECK_EQ(Ago(1000, 1030), std::string("just now"));
    CHECK_EQ(Ago(1000, 1000 + 4 * 60 + 59), std::string("4m ago"));
    CHECK_EQ(Ago(1000, 1000 + 2 * 3600), std::string("2h ago"));
    CHECK_EQ(Ago(1000, 1000 + 3 * 86400 + 5), std::string("3d ago"));
    CHECK_EQ(Ago(0, 1000), std::string("never"));
}

static void testParseSnapshot() {
    auto j = nlohmann::json::parse(R"({
        "monitor": "weather", "kind": "weather", "title": "Weather",
        "updated": "2026-10-10T18:07:31Z", "error": "site down", "subtitle": "Mazda3 · Automatic", "timely": true,
        "blocks": [
            {"type": "stats", "title": "Bristol", "stats": [
                {"label": "Now", "value": "13°C", "detail": "Clear", "tone": "accent", "series": [0, 0.4, 1.5]}
            ]},
            {"type": "table", "columns": [{"title": "Day"}, {"title": "High", "align": "right", "fill": true}],
             "rows": [[{"text": "Today", "url": "https://example.com", "detail": "dry"}, {"text": "15°", "tone": "bad"}]]},
            {"type": "list", "items": [{"title": "Car", "image": "https://example.com/a.jpg", "tone": "warn"}]},
            {"type": "text", "text": "hello", "unknown": {"nested": true}},
            {"type": "checklist", "addHint": "Add a task…", "items": [{"id": "a1", "title": "Bins", "done": true}, {"id": "b2", "title": "Post"}]}
        ],
        "summary": [{"label": "Now", "value": "13°C", "tone": "good", "series": [1, 2]}, {"label": "Rain"}],
        "summaryStyle": "chips"
    })");
    Snapshot s = ParseSnapshot(j);
    CHECK_EQ(s.monitor, std::string("weather"));
    CHECK_EQ(s.error, std::string("site down"));
    CHECK_EQ(s.subtitle, std::string("Mazda3 · Automatic"));
    CHECK(s.timely);
    CHECK_EQ(s.updated, int64_t(1791655651));
    CHECK_EQ(s.blocks.size(), size_t(5));
    CHECK_EQ(s.blocks[4].addHint, std::string("Add a task…"));
    CHECK_EQ(s.blocks[4].items[0].id, std::string("a1"));
    CHECK(s.blocks[4].items[0].done);
    CHECK(!s.blocks[4].items[1].done);
    CHECK_EQ(s.blocks[0].stats.size(), size_t(1));
    CHECK_EQ(s.blocks[0].stats[0].value, std::string("13°C"));
    CHECK(s.blocks[0].stats[0].tone == Tone::Accent);
    CHECK_EQ(s.blocks[0].stats[0].series.size(), size_t(3));
    CHECK(s.blocks[1].columns[1].align == Align::Right);
    CHECK(!s.blocks[1].columns[0].fill);
    CHECK(s.blocks[1].columns[1].fill);
    CHECK_EQ(s.summary.size(), size_t(2));
    CHECK_EQ(s.summaryStyle, std::string("chips"));
    CHECK_EQ(s.summary[0].value, std::string("13°C"));
    CHECK(s.summary[0].tone == Tone::Good);
    CHECK_EQ(s.summary[0].series.size(), size_t(2));
    CHECK_EQ(s.blocks[1].rows[0][0].url, std::string("https://example.com"));
    CHECK_EQ(s.blocks[1].rows[0][0].detail, std::string("dry"));
    CHECK(s.blocks[1].rows[0][1].detail.empty());
    CHECK(s.blocks[1].rows[0][1].tone == Tone::Bad);
    CHECK(s.blocks[2].items[0].tone == Tone::Warn);
    CHECK_EQ(s.blocks[3].text, std::string("hello"));

    // Wrong types and missing fields don't throw.
    Snapshot odd = ParseSnapshot(nlohmann::json::parse(R"({"monitor": 5, "blocks": {"x": 1}})"));
    CHECK(!odd.timely);
    CHECK(odd.monitor.empty());
    CHECK(odd.blocks.empty());
    CHECK_EQ(ParseSnapshot(nlohmann::json::parse(R"({"updated": "0001-01-01T00:00:00Z"})")).updated, int64_t(0));
}

static void testUseCompact() {
    CHECK(UseCompact(false, 1242, 906, 1));   // the usual window
    CHECK(!UseCompact(false, 5120, 1400, 1));  // maximised
    CHECK(UseCompact(false, 800, 1400, 1));   // narrow
    CHECK(UseCompact(false, 2484, 1812, 2));   // the usual window, at 200%
    // Near the boundary, the current choice sticks.
    CHECK(UseCompact(true, 1600, 1120, 1));
    CHECK(!UseCompact(false, 1600, 1080, 1));
    CHECK(!UseCompact(true, 1600, 1150, 1));
}

static void testFitPanels() {
    // Rows of 50, gaps of 10: 300 high holds all four rows (240) with 60 over.
    std::vector<Panel> p = {{50, 150, 6}, {50, 600, 6}, {50, 80, 6}, {50, 100, 6}};
    auto shows = [](const Fit& f) {
        std::string s;
        for (Show v : f.show) s += v == Show::Full ? 'F' : v == Show::Clipped ? 'C' : 'r';
        return s;
    };
    // Plenty of room: everything opens.
    CHECK_EQ(shows(FitPanels(p, 2000, 10, 40)), std::string("FFFF"));
    // The first opens (+100), the second wouldn't fit, the third and
    // fourth open (+30, +50); the second takes what's left:
    // 500 - 420 - 6 = 74.
    Fit f = FitPanels(p, 500, 10, 40);
    CHECK_EQ(shows(f), std::string("FCFF"));
    CHECK_EQ(f.body, 74.0f);
    // At 400 the fourth no longer fits, and 24 is too little left over for
    // a useful scrolling panel.
    CHECK_EQ(shows(FitPanels(p, 400, 10, 40)), std::string("FrFr"));
    // Pins win: a closed one stays closed and an open one opens first,
    // even when it doesn't fit.
    p[0].pin = Pin::Closed;
    p[1].pin = Pin::Open;
    CHECK_EQ(shows(FitPanels(p, 500, 10, 40)), std::string("rFrr"));
    // Unmeasured panels stay as rows.
    CHECK_EQ(shows(FitPanels({{50, 0, 6}, {50, 100, 6}}, 2000, 10, 40)), std::string("rF"));
}

static void testGpuUtilisation() {
    // Two processes on the 3D engine add up; the video engine is separate;
    // the busiest engine wins. Junk names are ignored.
    std::vector<std::pair<std::string, double>> in = {
        {"pid_100_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D", 30},
        {"pid_200_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D", 15},
        {"pid_300_luid_0x00000000_0x0000D1A5_phys_0_eng_3_engtype_VideoDecode", 40},
        {"something else", 99},
    };
    CHECK_EQ(GpuUtilisation(in), 45.0f);
    in.push_back({"pid_400_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D", 90});
    CHECK_EQ(GpuUtilisation(in), 100.0f);  // capped
    CHECK_EQ(GpuUtilisation({}), 0.0f);
}

static void testLocalPanels() {
    constexpr uint64_t GB = 1024ull * 1024 * 1024;
    CHECK_EQ(FormatBytes(512ull * 1024 * 1024), std::string("512 MB"));
    CHECK_EQ(FormatBytes(14 * GB + GB / 5), std::string("14.2 GB"));
    CHECK_EQ(FormatBytes(2048 * GB), std::string("2.0 TB"));
    CHECK(LoadTone(84) == Tone::None);
    CHECK(LoadTone(85) == Tone::Warn);
    CHECK(LoadTone(95) == Tone::Bad);

    PcHistory h;
    for (int i = 0; i < 70; i++) h.Add({float(i), 50, 8 * GB, 32 * GB, 0, 0, "RTX"});
    Snapshot pc = h.Build(1000);
    CHECK(IsLocal(pc));
    CHECK_EQ(pc.title, std::string("This PC"));
    CHECK_EQ(pc.blocks.size(), size_t(1));
    const auto& st = pc.blocks[0].stats;
    CHECK_EQ(st.size(), size_t(4));
    CHECK_EQ(st[0].value, std::string("69%"));
    CHECK_EQ(st[0].series.size(), PcHistory::kLength);  // a minute kept
    CHECK_EQ(st[0].top, 100.0f);                        // percentages on a fixed scale
    CHECK_EQ(st[1].value, std::string("25%"));
    CHECK_EQ(st[1].detail, std::string("8.0 GB / 32.0 GB"));
    CHECK_EQ(st[2].detail, std::string("RTX"));
    CHECK_EQ(st[3].value, std::string("–"));  // no VRAM reading
    CHECK(st[3].tone == Tone::Muted);
    CHECK_EQ(pc.summary.size(), size_t(4));

    Snapshot d = DrivesSnapshot({{"C:\\", "", 1000 * GB, 40 * GB}, {"D:\\", "Games", 2000 * GB, 1000 * GB}, {"E:\\", "", 0, 0}}, 1000);
    CHECK_EQ(d.blocks[0].rows.size(), size_t(2));  // the empty drive is skipped
    CHECK_EQ(d.blocks[0].rows[0][0].text, std::string("C:"));
    CHECK_EQ(d.blocks[0].rows[0][1].text, std::string("Local Disk"));
    CHECK_EQ(d.blocks[0].rows[0][2].text, std::string("96%"));
    CHECK(d.blocks[0].rows[0][2].tone == Tone::Bad);
    CHECK_EQ(d.blocks[0].rows[1][2].text, std::string("50%"));
    CHECK(d.blocks[0].columns[1].fill);
    CHECK_EQ(d.summary.size(), size_t(2));
    CHECK_EQ(d.summary[1].detail, std::string("1000.0 GB free"));
    CHECK_EQ(DrivesSnapshot({}, 0).blocks[0].type, std::string("text"));
}

static void testMedia() {
    CHECK_EQ(AppName("Spotify.exe"), std::string("Spotify"));
    CHECK_EQ(AppName("MSEdge"), std::string("Edge"));
    CHECK_EQ(AppName("308046B0AF4A39CB"), std::string("Firefox"));
    CHECK_EQ(AppName("Microsoft.ZuneMusic_8wekyb3d8bbwe!Microsoft.ZuneMusic"), std::string("Media Player"));
    CHECK_EQ(AppName("Tidal.exe"), std::string("Tidal"));
    CHECK_EQ(AppName("Some.App_abc!App"), std::string("Some.App_abc"));
    CHECK_EQ(StripEmoji("Big win \xF0\x9F\x98\x82\xF0\x9F\x94\xA5 tonight \xE2\x9D\xA4\xEF\xB8\x8F"),
             std::string("Big win tonight"));
    CHECK_EQ(StripEmoji("Café – Σίγουρα, Привет"), std::string("Café – Σίγουρα, Привет"));  // kept
    CHECK_EQ(FormatTime(185), std::string("3:05"));
    CHECK_EQ(FormatTime(3723), std::string("1:02:03"));
    CHECK_EQ(FormatTime(-4), std::string("0:00"));

    CHECK(MediaSnapshot({}, 0).blocks.empty());  // nothing playing: no panel
    NowPlaying np;
    np.active = true;
    np.title = "Song";
    np.artist = "Band";
    np.album = "Record";
    np.app = "Spotify";
    np.image = "media:1";
    np.media.playing = true;
    np.media.duration = 200;
    Snapshot s = MediaSnapshot(np, 1000);
    CHECK(IsLocal(s));
    CHECK_EQ(s.blocks[0].type, std::string("media"));
    CHECK_EQ(s.blocks[0].items[0].subtitle, std::string("Band"));
    CHECK_EQ(s.blocks[0].items[0].detail, std::string("Record · Spotify"));
    CHECK_EQ(s.blocks[0].items[0].image, std::string("media:1"));
    CHECK_EQ(s.blocks[0].media.duration, 200.0);
    CHECK_EQ(s.summary[0].label, std::string("Now playing"));
    CHECK_EQ(s.summary[0].detail, std::string("Band · Spotify"));
    np.media.playing = false;
    CHECK_EQ(MediaSnapshot(np, 1000).summary[0].label, std::string("Paused"));
}

static void testArrange() {
    using V = std::vector<std::string>;
    V defaults = {"pc", "calendar", "todo", "stocks", "drives"};
    CHECK(Arrange(defaults, {}) == defaults);
    CHECK(Arrange(defaults, {"stocks", "pc", "drives", "calendar", "todo"}) == V({"stocks", "pc", "drives", "calendar", "todo"}));
    // A new panel (media, after pc by default) slots in after pc; a removed
    // one (gone) is dropped.
    defaults = {"pc", "media", "calendar", "todo", "stocks", "drives"};
    CHECK(Arrange(defaults, {"stocks", "pc", "gone", "drives", "calendar", "todo"}) ==
          V({"stocks", "pc", "media", "drives", "calendar", "todo"}));
    // A new first panel goes first.
    CHECK(Arrange({"new", "a", "b"}, {"b", "a"}) == V({"new", "b", "a"}));

    V order = {"a", "b", "c", "d"};
    CHECK(Move(order, "d", "b", false) == V({"a", "d", "b", "c"}));
    CHECK(Move(order, "a", "c", true) == V({"b", "c", "a", "d"}));
    CHECK(Move(order, "b", "b", true) == order);
    CHECK(Move(order, "x", "b", true) == order);
}

static void testOled() {
    // Eight points round a circle, three minutes apart, then round again.
    std::set<std::pair<float, float>> points;
    for (int64_t t = 0; t < 8 * 180; t += 180) points.insert(OledShift(t, 6));
    CHECK_EQ(points.size(), size_t(8));
    CHECK(OledShift(0, 6) == std::make_pair(6.0f, 0.0f));
    CHECK(OledShift(179, 6) == OledShift(0, 6));
    CHECK(OledShift(2 * 180, 6) == std::make_pair(0.0f, 6.0f));
    CHECK(OledShift(8 * 180, 6) == OledShift(0, 6));
    for (const auto& [x, y] : points) CHECK(x * x + y * y <= 37);

    CHECK(!OledSwapSides(0));
    CHECK(OledSwapSides(3600));
    CHECK(!OledSwapSides(7200 + 59));

    CHECK(!OledDim(1000, 1000, 0));
    CHECK(!OledDim(1000 + 599, 1000, 0));
    CHECK(OledDim(1000 + 600, 1000, 0));
    CHECK(!OledDim(1000 + 600, 1000, 1000 + 700));  // woken by something new
}

static void testParseEvent() {
    Event e = ParseEvent(nlohmann::json::parse(
        R"({"id": 42, "monitor": "nfl", "key": "final:1", "time": "2026-10-10T18:07:31Z",
            "title": "Final: TB 24 – 16 DAL", "priority": "high", "quiet": true})"));
    CHECK_EQ(e.id, int64_t(42));
    CHECK_EQ(e.title, std::string("Final: TB 24 – 16 DAL"));
    CHECK(e.priority == Priority::High);
    CHECK(e.quiet);
    CHECK(ParseEvent(nlohmann::json::object()).priority == Priority::Normal);
}

static Snapshot snap(const std::string& id) {
    Snapshot s;
    s.monitor = id;
    s.title = "T" + id;
    return s;
}

static void testModel() {
    Model m;
    m.BeginSync();
    m.ApplySnapshot(snap("a"));
    m.ApplySnapshot(snap("b"));
    m.EndSync();
    m.ApplySnapshot(snap("c"));  // a live update for a new monitor
    CHECK_EQ(m.Snapshots().size(), size_t(3));

    // Reconnect: the server now has b and a, in that order, and no c.
    m.BeginSync();
    m.ApplySnapshot(snap("b"));
    m.ApplySnapshot(snap("a"));
    m.EndSync();
    CHECK_EQ(m.Snapshots().size(), size_t(2));
    CHECK_EQ(m.Snapshots()[0].monitor, std::string("b"));
    CHECK_EQ(m.Snapshots()[1].monitor, std::string("a"));
    CHECK_EQ(m.TitleOf("a"), std::string("Ta"));
    CHECK_EQ(m.TitleOf("zzz"), std::string("zzz"));

    Event e;
    e.id = 2;
    CHECK(m.AddEvent(e));
    CHECK(!m.AddEvent(e));
    e.id = 5;
    CHECK(m.AddEvent(e));
    e.id = 3;
    CHECK(m.AddEvent(e));
    CHECK_EQ(m.Events().front().id, int64_t(5));
    CHECK_EQ(m.Events()[1].id, int64_t(3));
    CHECK_EQ(m.LastEventID(), int64_t(5));

    for (int i = 10; i < 10 + int(Model::kMaxEvents) + 5; i++) {
        e.id = i;
        m.AddEvent(e);
    }
    CHECK_EQ(m.Events().size(), Model::kMaxEvents);

    // Clearing: one, then all; neither comes back when history is replayed,
    // but newer events still arrive.
    Model c;
    for (int i = 1; i <= 4; i++) {
        e.id = i;
        c.AddEvent(e);
    }
    c.Clear(3);
    CHECK_EQ(c.Events().size(), size_t(3));
    e.id = 3;
    CHECK(!c.AddEvent(e));
    CHECK(c.GetCleared().ids.count(3));
    c.ClearAll();
    CHECK(c.Events().empty());
    CHECK_EQ(c.GetCleared().through, int64_t(4));
    CHECK(c.GetCleared().ids.empty());
    e.id = 2;
    CHECK(!c.AddEvent(e));
    e.id = 5;
    CHECK(c.AddEvent(e));
    CHECK_EQ(c.LastEventID(), int64_t(5));

    // After a restart: what was cleared before is dropped, the rest kept.
    Model r;
    for (int i = 1; i <= 6; i++) {
        e.id = i;
        r.AddEvent(e);
    }
    Model::Cleared saved;
    saved.through = 2;
    saved.ids = {1, 5};
    r.SetCleared(saved);
    CHECK_EQ(r.Events().size(), size_t(3));
    CHECK_EQ(r.Events().front().id, int64_t(6));
    CHECK_EQ(r.Events().back().id, int64_t(3));
    CHECK_EQ(r.GetCleared().ids.size(), size_t(1));
    for (int i = 100; i < 100 + int(Model::kMaxCleared) + 10; i++) r.Clear(i);
    CHECK_EQ(r.GetCleared().ids.size(), Model::kMaxCleared);
    CHECK(!r.GetCleared().ids.count(5));
}

static void testSse() {
    SseParser p;
    std::vector<SseMessage> out;
    // Split mid-line and mid-CRLF, with a heartbeat comment and a
    // multi-line data field.
    p.Feed("id: 7\r\nevent: event\r", out);
    p.Feed("\ndata: {\"a\":1}\r\n\r\n: ping\n\n", out);
    p.Feed("event: snapshot\ndata: x\ndata:y\n", out);
    CHECK_EQ(out.size(), size_t(1));
    p.Feed("\n", out);
    p.Feed("data: no event name\n\n", out);
    p.Feed("event: empty\n\n", out);  // no data: nothing dispatched
    CHECK_EQ(out.size(), size_t(3));
    CHECK_EQ(out[0].id, std::string("7"));
    CHECK_EQ(out[0].event, std::string("event"));
    CHECK_EQ(out[0].data, std::string("{\"a\":1}"));
    CHECK_EQ(out[1].event, std::string("snapshot"));
    CHECK_EQ(out[1].data, std::string("x\ny"));
    CHECK_EQ(out[1].id, std::string("7"));  // the ID carries over
    CHECK_EQ(out[2].event, std::string("message"));
}

static void testPrivate() {
    Snapshot s = ParseSnapshot(nlohmann::json::parse(R"({
        "monitor": "stocks",
        "summary": [{"label": "Today", "value": "+£10", "private": true}, {"label": "AMD", "value": "+1%"}],
        "blocks": [
            {"type": "stats", "private": true, "stats": [{"label": "Portfolio", "value": "£1,000"}]},
            {"type": "stats", "stats": [{"label": "Mine", "value": "£5", "private": true}, {"label": "Public", "value": "1"}]},
            {"type": "stats", "stats": [{"label": "Only mine", "value": "£5", "private": true}]},
            {"type": "table", "private": true, "columns": [{"title": "Day"}]},
            {"type": "table", "columns": [{"title": "Symbol"}]}
        ]})"));
    CHECK(HasPrivate(s));
    CHECK(s.blocks[0].isPrivate);
    CHECK(s.summary[0].isPrivate);

    Snapshot hidden = WithoutPrivate(s);
    CHECK(!HasPrivate(hidden));
    CHECK_EQ(hidden.summary.size(), size_t(1));
    CHECK_EQ(hidden.summary[0].label, std::string("AMD"));
    CHECK_EQ(hidden.blocks.size(), size_t(2));
    CHECK_EQ(hidden.blocks[0].stats.size(), size_t(1));
    CHECK_EQ(hidden.blocks[0].stats[0].label, std::string("Public"));
    CHECK_EQ(hidden.blocks[1].columns[0].title, std::string("Symbol"));

    CHECK(!HasPrivate(ParseSnapshot(nlohmann::json::parse(R"({"blocks": [{"type": "text", "text": "hi"}]})"))));
}

// Recorder is a canvas that keeps the bounds of what's drawn.
struct Recorder : Canvas {
    float minX = 1e9f, maxX = -1e9f, minY = 1e9f, maxY = -1e9f;
    int shapes = 0;
    void see(Vec2f p, float rx, float ry) {
        minX = std::min(minX, p.x - rx), maxX = std::max(maxX, p.x + rx);
        minY = std::min(minY, p.y - ry), maxY = std::max(maxY, p.y + ry);
        shapes++;
    }
    void see(Vec2f p, float r) { see(p, r, r); }
    void Circle(Vec2f c, float r, Rgba) override { see(c, r); }
    // Rotated ellipses are small, nearly round or barely turned: their
    // radii do.
    void Ellipse(Vec2f c, float rx, float ry, float, Rgba) override { see(c, rx, ry); }
    void Line(Vec2f a, Vec2f b, float w, Rgba) override { see(a, w / 2), see(b, w / 2); }
    void Polygon(const Vec2f* p, int n, Rgba) override {
        for (int i = 0; i < n; i++) see(p[i], 0);
    }
};

static void testWeatherIcons() {
    // Each stays inside its square and draws something; unknown names
    // draw nothing.
    IconColours col;
    for (const char* n : {"sun", "moon", "sun-cloud", "moon-cloud", "cloud", "fog", "drizzle", "rain", "showers", "snow", "storm"}) {
        CHECK(IsWeatherIcon(n));
        Recorder r;
        DrawWeatherIcon(r, n, {10, 20}, 50, col);
        CHECK(r.shapes > 0);
        CHECK(r.minX >= 10 && r.maxX <= 60 && r.minY >= 20 && r.maxY <= 70);
    }
    CHECK(!IsWeatherIcon("tornado"));
    Recorder none;
    DrawWeatherIcon(none, "tornado", {0, 0}, 50, col);
    CHECK_EQ(none.shapes, 0);
    Snapshot snap = ParseSnapshot(nlohmann::json::parse(
        R"({"summary": [{"label": "13:00", "value": "18°", "icon": "rain", "level": 0.6}, {"level": 3}, {}, {"level": 0}]})"));
    CHECK_EQ(snap.summary[0].icon, std::string("rain"));
    CHECK(std::fabs(snap.summary[0].level - 0.6f) < 1e-6f);
    CHECK_EQ(snap.summary[1].level, 1.0f);  // clamped
    CHECK_EQ(snap.summary[2].level, 0.0f);
    CHECK(!snap.summary[2].hasLevel);
    CHECK(snap.summary[3].hasLevel);  // 0% still draws
}

static void testCat() {
    // Over two whole episodes, at a narrow and a wide column, she stays on
    // the floor and inside her space: 68 units tall (4 below the floor) and
    // the column's width. She sleeps most of the time, but wakes, yawns,
    // walks and turns round.
    for (float half : {120.0f, 150.0f}) {
        bool walked = false, turned = false, yawned = false;
        int asleep = 0, frames = 0;
        float lastX = CatAt(0, half).x;
        for (double t = 0; t < 2 * kEpisode; t += 1.0 / 20, frames++) {
            Cat c = CatAt(t, half);
            for (const Vec2f& paw : c.pose.paw) CHECK(paw.y <= 0.01f);
            CHECK(std::isfinite(c.x) && std::fabs(c.facing) <= 1);
            walked = walked || std::fabs(c.x - lastX) > 0.01f;
            turned = turned || (c.turn > 0.4f && c.turn < 0.6f);
            yawned = yawned || c.pose.yawn > 0.9f;
            asleep += c.asleep >= 1;
            lastX = c.x;
            Recorder r;
            DrawCat(r, c, t, {0, 0}, 1);
            CHECK(r.shapes > 30);
            CHECK(r.maxY <= 4);
            CHECK(r.minY >= -64);
            CHECK(r.minX >= -half && r.maxX <= half);
        }
        CHECK(walked);
        CHECK(yawned);
        CHECK(asleep > frames * 3 / 4);
        (void)turned;  // depends on where she happens to go
    }
    // She never jumps: from one frame to the next she moves a little, and
    // only turns round by turning, at the end of which she faces the other
    // way. Over many episodes, including ones she doesn't walk in.
    for (float half : {60.0f, 150.0f}) {
        for (double t = 0; t < 40 * kEpisode; t += 0.05) {
            Cat a = CatAt(t, half), b = CatAt(t + 0.05, half);
            CHECK(std::fabs(a.x - b.x) < 3);
            CHECK(std::fabs(a.pose.head.y - b.pose.head.y) < 3);
            if (a.facing != b.facing) CHECK(a.turn > 0.9f);
        }
    }
    // Asked out of order, e.g. after a jump in time, she's the same.
    Cat later = CatAt(37 * kEpisode + 3, 150);
    CatAt(2 * kEpisode, 150);
    Cat again = CatAt(37 * kEpisode + 3, 150);
    CHECK(later.x == again.x && later.facing == again.facing);
}

int main() {
    testCat();
    testWeatherIcons();
    testPrivate();
    testParseTime();
    testAgo();
    testParseSnapshot();
    testUseCompact();
    testFitPanels();
    testGpuUtilisation();
    testLocalPanels();
    testMedia();
    testArrange();
    testOled();
    testParseEvent();
    testModel();
    testSse();
    if (failures) {
        std::printf("%d failures\n", failures);
        return 1;
    }
    std::printf("all tests passed\n");
    return 0;
}
