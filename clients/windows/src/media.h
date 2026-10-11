// Reads what's playing from Windows' media sessions (the same source as the
// media pop-up on the volume keys) and controls it, for the "Now playing"
// panel. Any app that reports to Windows shows: Spotify, browsers and so on.
#pragma once

#include <atomic>
#include <condition_variable>
#include <functional>
#include <mutex>
#include <thread>

#include "images.h"
#include "model.h"

namespace ks {

enum class MediaCommand { None, Previous, TogglePlay, Next };

class MediaWatcher {
public:
    // wake is called after each new reading. Cover art goes to images.
    MediaWatcher(std::function<void()> wake, ImageCache* images);
    ~MediaWatcher();

    // Want says the panel is on screen; reading stops a few seconds after
    // the last call.
    void Want();
    // Take gives the latest panel if it's changed since the last call.
    bool Take(Snapshot& out);
    // Command asks the current app to skip or play/pause.
    void Command(MediaCommand c);

private:
    void run();

    std::function<void()> wake_;
    ImageCache* images_;
    std::thread thread_;
    std::mutex mu_;
    std::condition_variable cv_;
    bool stop_ = false;
    MediaCommand command_ = MediaCommand::None;
    Snapshot latest_;
    bool fresh_ = false;
    std::atomic<unsigned long long> wanted_{0};
};

}  // namespace ks
