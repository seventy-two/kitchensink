// Reads this PC's CPU, memory, GPU and drive use on a background thread,
// for the local panels (see local.h).
#pragma once

#include <atomic>
#include <condition_variable>
#include <functional>
#include <mutex>
#include <thread>
#include <vector>

#include "model.h"

namespace ks {

class LocalStats {
public:
    // wake is called from the background thread after each new reading.
    explicit LocalStats(std::function<void()> wake);
    ~LocalStats();

    // Want says the panels are on screen. Reading stops a few seconds after
    // the last call, so a hidden window costs nothing.
    void Want();
    // Take gives the latest panels, "This PC" then "Drives", if they've
    // changed since the last call.
    bool Take(std::vector<Snapshot>& out);

private:
    void run();

    std::function<void()> wake_;
    std::thread thread_;
    std::mutex mu_;
    std::condition_variable cv_;
    bool stop_ = false;
    std::vector<Snapshot> latest_;
    bool fresh_ = false;
    std::atomic<unsigned long long> wanted_{0};  // GetTickCount64 of the last Want
};

}  // namespace ks
