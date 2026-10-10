// Tests for the dashboard's portable code. Build and run with build.bat, or
// on Linux: g++ -std=c++17 -I src -I third_party tests/tests.cpp src/model.cpp src/sse.cpp
#include <cstdio>
#include <string>

#include "../src/model.h"
#include "../src/sse.h"

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
        "updated": "2026-10-10T18:07:31Z", "error": "site down",
        "blocks": [
            {"type": "stats", "title": "Bristol", "stats": [
                {"label": "Now", "value": "13°C", "detail": "Clear", "tone": "accent", "series": [0, 0.4, 1.5]}
            ]},
            {"type": "table", "columns": [{"title": "Day"}, {"title": "High", "align": "right"}],
             "rows": [[{"text": "Today", "url": "https://example.com"}, {"text": "15°", "tone": "bad"}]]},
            {"type": "list", "items": [{"title": "Car", "image": "https://example.com/a.jpg", "tone": "warn"}]},
            {"type": "text", "text": "hello", "unknown": {"nested": true}}
        ]
    })");
    Snapshot s = ParseSnapshot(j);
    CHECK_EQ(s.monitor, std::string("weather"));
    CHECK_EQ(s.error, std::string("site down"));
    CHECK_EQ(s.updated, int64_t(1791655651));
    CHECK_EQ(s.blocks.size(), size_t(4));
    CHECK_EQ(s.blocks[0].stats.size(), size_t(1));
    CHECK_EQ(s.blocks[0].stats[0].value, std::string("13°C"));
    CHECK(s.blocks[0].stats[0].tone == Tone::Accent);
    CHECK_EQ(s.blocks[0].stats[0].series.size(), size_t(3));
    CHECK(s.blocks[1].columns[1].align == Align::Right);
    CHECK_EQ(s.blocks[1].rows[0][0].url, std::string("https://example.com"));
    CHECK(s.blocks[1].rows[0][1].tone == Tone::Bad);
    CHECK(s.blocks[2].items[0].tone == Tone::Warn);
    CHECK_EQ(s.blocks[3].text, std::string("hello"));

    // Wrong types and missing fields don't throw.
    Snapshot odd = ParseSnapshot(nlohmann::json::parse(R"({"monitor": 5, "blocks": {"x": 1}})"));
    CHECK(odd.monitor.empty());
    CHECK(odd.blocks.empty());
    CHECK_EQ(ParseSnapshot(nlohmann::json::parse(R"({"updated": "0001-01-01T00:00:00Z"})")).updated, int64_t(0));
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

int main() {
    testParseTime();
    testAgo();
    testParseSnapshot();
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
