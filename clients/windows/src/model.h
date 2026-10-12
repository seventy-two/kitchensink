// The dashboard's data: snapshots and events as the server sends them, and
// the state built from the stream. Nothing here depends on Windows, so it's
// unit tested on any platform.
#pragma once

#include <cstdint>
#include <deque>
#include <set>
#include <string>
#include <utility>
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
    bool isPrivate = false;  // a personal figure, e.g. money, hidden on request
    std::string icon;        // a picture by the value, e.g. "rain"
    float level = 0;         // 0 to 1, a faint chart behind, e.g. the chance of rain
};

struct Column {
    std::string title;
    Align align = Align::Left;
    bool fill = false;  // takes the table's spare width
};

struct Cell {
    std::string text, url;
    Tone tone = Tone::None;
    std::string detail;  // a smaller, muted line under the text
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
    bool isPrivate = false;  // personal figures, hidden on request
    Media media;
    std::vector<Stat> stats;
    std::vector<Column> columns;
    std::vector<std::vector<Cell>> rows;
    std::vector<Item> items;
};

struct Snapshot {
    std::string monitor, kind, title, error;
    std::string subtitle;  // what it's watching, when that isn't obvious
    bool timely = false;   // when it was updated matters at the moment
    int64_t updated = 0;  // Unix seconds; 0 if never polled
    std::vector<Block> blocks;
    // The few things most worth seeing at a glance, shown in place of the
    // blocks when space is short. Older servers don't send it.
    std::vector<Stat> summary;
    std::string summaryStyle;  // "" for tiles, or "chips"
};

struct Event {
    int64_t id = 0;
    std::string monitor, key, title, body, url, image;
    int64_t time = 0;  // Unix seconds
    Priority priority = Priority::Normal;
    bool quiet = false;
};

Snapshot ParseSnapshot(const nlohmann::json& j);

// HasPrivate says whether a snapshot has any private blocks or stats.
bool HasPrivate(const Snapshot& s);
// WithoutPrivate is a snapshot with its private blocks and stats left out,
// for when the user has hidden them.
Snapshot WithoutPrivate(Snapshot s);
Event ParseEvent(const nlohmann::json& j);
Priority ParsePriority(const std::string& s);

// ParseTime parses an RFC 3339 time such as "2026-10-10T19:02:00.69+01:00"
// into Unix seconds. It returns 0 if the time can't be parsed.
int64_t ParseTime(const std::string& s);

// Ago describes how long before now a time was: "just now", "4m ago",
// "2h ago", "3d ago".
std::string Ago(int64_t then, int64_t now);

// Arrange orders panel IDs by a saved order. Panels in saved come in that
// order; ones it doesn't mention (e.g. added since) keep their place after
// the panel they follow by default. IDs in saved that no longer exist are
// ignored.
std::vector<std::string> Arrange(const std::vector<std::string>& defaults, const std::vector<std::string>& saved);

// Move returns order with id moved to just before target, or just after it
// when after is set. Unknown IDs leave it unchanged.
std::vector<std::string> Move(std::vector<std::string> order, const std::string& id, const std::string& target,
                              bool after);

// OLED care: small, regular changes so nothing sits on the same pixels for
// hours on an OLED screen. Times are Unix seconds.
//
// OledShift is how far to move the whole window's contents at time t: one
// of eight points on a circle of radius r, moving on every three minutes.
std::pair<float, float> OledShift(int64_t t, float r);
// OledSwapSides says whether the side column swaps sides at time t, which
// it does every other hour.
bool OledSwapSides(int64_t t);
// OledDim says whether to dim at time now: ten minutes after the last
// activity (the mouse over the window), unless something new has woken it
// until wakeUntil.
bool OledDim(int64_t now, int64_t lastActivity, int64_t wakeUntil);

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
    // AddEvent stores an event and reports whether it was new. Cleared
    // ones aren't stored again, e.g. when history is replayed.
    bool AddEvent(Event e);

    // Clear removes an event from Recent; ClearAll removes every one so
    // far. They stay cleared through reconnects and, via Cleared and
    // SetCleared, restarts.
    void Clear(int64_t id);
    void ClearAll();
    // Cleared is what's been cleared: every event up to through, and ids.
    struct Cleared {
        int64_t through = 0;
        std::set<int64_t> ids;
    };
    const Cleared& GetCleared() const { return cleared_; }
    void SetCleared(Cleared c);

    const std::vector<Snapshot>& Snapshots() const { return snapshots_; }
    // Events are newest first.
    const std::deque<Event>& Events() const { return events_; }
    int64_t LastEventID() const { return lastEventID_; }
    // TitleOf returns a monitor's title, or its ID if it has no snapshot.
    std::string TitleOf(const std::string& monitor) const;

    static constexpr size_t kMaxEvents = 300;
    // At most this many single cleared events are remembered, the newest.
    static constexpr size_t kMaxCleared = 500;

private:
    std::vector<Snapshot> snapshots_;
    std::deque<Event> events_;
    int64_t lastEventID_ = 0;
    bool syncing_ = false;
    std::set<std::string> synced_;
    Cleared cleared_;
};

}  // namespace ks
