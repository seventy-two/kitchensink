// The dashboard's settings, kept in %APPDATA%\kitchensink\config.json. The
// token is encrypted with DPAPI, so only this Windows user can read it.
#pragma once

#include <string>

#include "model.h"

namespace ks {

struct Config {
    std::string url;    // e.g. https://example.com/kitchensink
    std::string token;
    // Events below this priority don't raise a Windows notification.
    Priority minPriority = Priority::Normal;
    bool notify = true;
};

// LoadConfig reads the settings, leaving defaults for anything missing.
Config LoadConfig();
bool SaveConfig(const Config& c);

// Start with Windows, via the current user's Run key.
bool StartsWithWindows();
void SetStartWithWindows(bool on);

}  // namespace ks
