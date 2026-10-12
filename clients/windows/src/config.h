// The dashboard's settings, kept in %APPDATA%\kitchensink\config.json. The
// token is encrypted with DPAPI, so only this Windows user can read it.
#pragma once

#include <set>
#include <string>
#include <vector>

#include "model.h"

namespace ks {

// OledCare guards OLED screens against burn-in, each part on its own.
struct OledCare {
    bool shift = false;  // move everything round a small circle every three minutes
    bool black = false;  // a true-black theme with dimmer text and fainter borders
    bool dim = false;    // dim after ten minutes without the mouse over the window
    bool swap = false;   // swap the Recent column's side every other hour
};

struct Config {
    std::string url;    // e.g. https://example.com/kitchensink
    std::string token;
    // Events below this priority don't raise a Windows notification.
    Priority minPriority = Priority::Normal;
    bool notify = true;
    OledCare oled;
    // A black cat asleep above the Recent column.
    bool cat = false;
    // Hides private figures, e.g. a portfolio's value, from the panels.
    bool hidePrivate = false;
    // In the compact layout, monitors opened or closed by hand. The rest
    // open by themselves while there's room.
    std::set<std::string> expanded, collapsed;
    // Panel order, as dragged; empty for the server's config order.
    std::vector<std::string> order;
};

// LoadConfig reads the settings, leaving defaults for anything missing.
Config LoadConfig();
bool SaveConfig(const Config& c);

// Start with Windows, via the current user's Run key.
bool StartsWithWindows();
void SetStartWithWindows(bool on);

}  // namespace ks
