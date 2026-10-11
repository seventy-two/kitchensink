// The dashboard's data: snapshots and events as the server sends them, and
// the state built from the stream. Nothing here depends on Windows, so it's
// unit tested on any platform.
#pragma once

#include <cstdint>
#include <deque>
#include <set>
#include <string>
#include <vector>

#include <nlohmann/json.hpp>

namespace ks {

enum class Tone { None, Good, Bad, Warn, Muted, Accent };
enum class Align { Left, Right, Center };
enum class Priority { Low, Normal, High };

struct Stat {
    std::string label, value, detail;
    Tone tone = Tone::None;
    std::vector<float> series;
    // top fixes the sparkline's scale, e.g. 100 for a percentage; 0 fits
    // it to the series.
    float top = 0;
};

struct Column {
    std::string title;
    Align align = Align::Left;
    bool fill = false;  // takes the table's spare width
};

struct Cell {
    std::string text, url;
    Tone tone = Tone::None;
};

struct Item {
    std::string title, subtitle, detail, url, image;
    Tone tone = Tone::None;
    // For checklists.
    std::string id;
    bool done = false;
};

// Media is what a "media" block is playing. Only local panels make these.
struct Media {
    bool playing = false;
    bool canPrevious = false, canToggle = false, canNext = false;
    double position = 0;  // seconds, as of asOf
    double duration = 0;  // seconds; 0 if unknown
    double asOf = 0;      // Unix seconds
};

// Block is one part of a monitor's panel. Only the fields for its type are
// set: "stats", "table", "list", "checklist", "media" (items[0] and media)
// or "text".
struct Block {
    std::string type, title, text;
    std::string addHint;  // a checklist takes new items when set
    Media media;
    std::vector<Stat> stats;
    std::vector<Column> columns;
    std::vector<std::vector<Cell>> rows;
    std::vector<Item> items;
};

struct Snapshot {
    std::string monitor, kind, title, error;
    std::string subtitle;  // what it's watching, when that isn't obvious
    int64_t updated = 0;  // Unix seconds; 0 if never polled
    std::vector<Block> blocks;
    // The few things most worth seeing at a glance, shown in place of the
    // blocks when space is short. Older servers don't send it.
    std::vector<Stat> summary;
};

struct Event {
    int64_t id = 0;
    std::string monitor, key, title, body, url, image;
    int64_t time = 0;  // Unix seconds
    Priority priority = Priority::Normal;
    bool quiet = false;
};

Snapshot ParseSnapshot(const nlohmann::json& j);
Event ParseEvent(const nlohmann::json& j);
Priority ParsePriority(const std::string& s);

// ParseTime parses an RFC 3339 time such as "2026-10-10T19:02:00.69+01:00"
// into Unix seconds. It returns 0 if the time can't be parsed.
int64_t ParseTime(const std::string& s);

// Ago describes how long before now a time was: "just now", "4m ago",
// "2h ago", "3d ago".
std::string Ago(int64_t then, int64_t now);

// UseCompact decides whether a window of this size, in pixels at this DPI
// scale, shows each monitor's summary rather than its full panel. It takes
// the current choice so that resizing near the boundary doesn't flicker.
bool UseCompact(bool compact, float width, float height, float scale);

// Show is how much of a monitor the compact layout shows: its summary row,
// its full panel, or its panel cut short to fit, scrolling inside.
enum class Show { Row, Full, Clipped };

// Pin is a choice made by hand in the compact layout.
enum class Pin { Auto, Open, Closed };

// Panel is one monitor's measured heights in the compact layout.
struct Panel {
    float row = 0;   // collapsed to its summary
    float full = 0;  // with its full panel; 0 if not measured yet
    // What clipping adds to the row besides the scrolling area itself.
    float overhead = 0;
    Pin pin = Pin::Auto;
};

struct Fit {
    std::vector<Show> show;
    float body = 0;  // height of the clipped panel's scrolling area
};

// FitPanels fills a column of height with panels, gap apart. Pinned ones
// are open or closed as chosen; the rest open in order while they fit
// whole. If space is left, the first that didn't fit takes it, scrolling,
// as long as that gives it at least minBody.
Fit FitPanels(const std::vector<Panel>& panels, float height, float gap, float minBody);

// Model is the dashboard's state, built from the stream.
class Model {
public:
    // BeginSync is called when the stream connects. Monitors the server
    // doesn't send a snapshot for before EndSync are dropped, so ones
    // removed from the config disappear.
    void BeginSync();
    void EndSync();

    void ApplySnapshot(Snapshot s);
    // AddEvent stores an event and reports whether it was new.
    bool AddEvent(Event e);

    const std::vector<Snapshot>& Snapshots() const { return snapshots_; }
    // Events are newest first.
    const std::deque<Event>& Events() const { return events_; }
    int64_t LastEventID() const { return lastEventID_; }
    // TitleOf returns a monitor's title, or its ID if it has no snapshot.
    std::string TitleOf(const std::string& monitor) const;

    static constexpr size_t kMaxEvents = 300;

private:
    std::vector<Snapshot> snapshots_;
    std::deque<Event> events_;
    int64_t lastEventID_ = 0;
    bool syncing_ = false;
    std::set<std::string> synced_;
};

}  // namespace ks
