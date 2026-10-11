#include "media.h"

#include <windows.h>

#include <algorithm>
#include <chrono>
#include <ctime>
#include <string>

#include <winrt/Windows.Foundation.Collections.h>
#include <winrt/Windows.Foundation.h>
#include <winrt/Windows.Media.Control.h>
#include <winrt/Windows.Storage.Streams.h>

#include "local.h"
#include "net.h"

namespace ks {

namespace {

using namespace winrt::Windows::Media::Control;
using winrt::Windows::Storage::Streams::Buffer;
using winrt::Windows::Storage::Streams::InputStreamOptions;

double seconds(winrt::Windows::Foundation::TimeSpan t) { return std::chrono::duration<double>(t).count(); }

double unixSeconds(winrt::clock::time_point t) {
    return std::chrono::duration<double>(winrt::clock::to_sys(t).time_since_epoch()).count();
}

// readCover reads a session's cover art into bytes for the image cache.
std::string readCover(const GlobalSystemMediaTransportControlsSessionMediaProperties& props) {
    auto ref = props.Thumbnail();
    if (!ref) return {};
    auto stream = ref.OpenReadAsync().get();
    uint32_t size = uint32_t(std::min<uint64_t>(stream.Size(), 8 << 20));
    Buffer buf(size);
    auto read = stream.ReadAsync(buf, size, InputStreamOptions::None).get();
    return std::string(reinterpret_cast<const char*>(read.data()), read.Length());
}

}  // namespace

MediaWatcher::MediaWatcher(std::function<void()> wake, ImageCache* images) : wake_(std::move(wake)), images_(images) {
    thread_ = std::thread([this] { run(); });
}

MediaWatcher::~MediaWatcher() {
    {
        std::lock_guard<std::mutex> lock(mu_);
        stop_ = true;
    }
    cv_.notify_all();
    thread_.join();
}

void MediaWatcher::Want() {
    unsigned long long before = wanted_.exchange(GetTickCount64());
    if (GetTickCount64() - before > 3000) {
        { std::lock_guard<std::mutex> lock(mu_); }
        cv_.notify_all();
    }
}

bool MediaWatcher::Take(Snapshot& out) {
    std::lock_guard<std::mutex> lock(mu_);
    if (!fresh_) return false;
    out = latest_;
    fresh_ = false;
    return true;
}

void MediaWatcher::Command(MediaCommand c) {
    {
        std::lock_guard<std::mutex> lock(mu_);
        command_ = c;
    }
    cv_.notify_all();
}

void MediaWatcher::run() {
    // WinRT calls block with .get(), which needs a multithreaded apartment.
    winrt::init_apartment(winrt::apartment_type::multi_threaded);
    GlobalSystemMediaTransportControlsSessionManager manager{nullptr};
    std::string track;  // title, artist and album of the cover we have
    std::string image;
    int covers = 0;
    // A paused track stays for a while after it last played, so pausing
    // from the panel doesn't make it vanish, then hides: a video paused
    // hours ago isn't worth the space.
    constexpr unsigned long long kPausedShownMs = 10 * 60 * 1000;
    std::string lastPlayed;
    unsigned long long lastPlayedAt = 0;

    std::unique_lock<std::mutex> lock(mu_);
    while (!stop_) {
        if (GetTickCount64() - wanted_.load() >= 3000 && command_ == MediaCommand::None) {
            cv_.wait(lock, [&] {
                return stop_ || command_ != MediaCommand::None || GetTickCount64() - wanted_.load() < 3000;
            });
            continue;
        }
        MediaCommand command = command_;
        command_ = MediaCommand::None;
        lock.unlock();

        NowPlaying np;
        try {
            if (!manager) manager = GlobalSystemMediaTransportControlsSessionManager::RequestAsync().get();
            // A playing app wins over Windows' current one, which is just
            // the most recent and may well be paused.
            auto playing = [](const GlobalSystemMediaTransportControlsSession& s) {
                return s.GetPlaybackInfo().PlaybackStatus() ==
                       GlobalSystemMediaTransportControlsSessionPlaybackStatus::Playing;
            };
            GlobalSystemMediaTransportControlsSession session = manager.GetCurrentSession();
            if (!session || !playing(session))
                for (const auto& s : manager.GetSessions())
                    if (playing(s)) {
                        session = s;
                        break;
                    }
            if (session) {
                switch (command) {
                case MediaCommand::Previous: session.TrySkipPreviousAsync().get(); break;
                case MediaCommand::TogglePlay: session.TryTogglePlayPauseAsync().get(); break;
                case MediaCommand::Next: session.TrySkipNextAsync().get(); break;
                case MediaCommand::None: break;
                }
                auto props = session.TryGetMediaPropertiesAsync().get();
                auto playback = session.GetPlaybackInfo();
                auto controls = playback.Controls();
                auto timeline = session.GetTimelineProperties();
                np.active = true;
                np.title = Narrow(props.Title());
                np.artist = Narrow(props.Artist());
                if (np.artist.empty()) np.artist = Narrow(props.AlbumArtist());
                np.album = Narrow(props.AlbumTitle());
                np.app = AppName(Narrow(session.SourceAppUserModelId()));
                np.media.playing =
                    playback.PlaybackStatus() == GlobalSystemMediaTransportControlsSessionPlaybackStatus::Playing;
                np.media.canPrevious = controls.IsPreviousEnabled();
                np.media.canNext = controls.IsNextEnabled();
                np.media.canToggle = controls.IsPlayPauseToggleEnabled();
                double start = seconds(timeline.StartTime());
                np.media.duration = std::max(0.0, seconds(timeline.EndTime()) - start);
                np.media.position = seconds(timeline.Position()) - start;
                np.media.asOf = unixSeconds(timeline.LastUpdatedTime());

                // The cover only changes with the track. Each gets a new
                // key, since the cache keeps images by key.
                std::string key = np.title + "\n" + np.artist + "\n" + np.album;
                if (key != track) {
                    track = key;
                    image.clear();
                    std::string bytes = readCover(props);
                    if (!bytes.empty()) {
                        image = "media:" + std::to_string(++covers);
                        images_->Provide(image, std::move(bytes));
                    }
                }
                np.image = image;

                std::string which = np.app + "\n" + key;
                if (np.media.playing) {
                    lastPlayed = which;
                    lastPlayedAt = GetTickCount64();
                } else if (which != lastPlayed || GetTickCount64() - lastPlayedAt > kPausedShownMs) {
                    np.active = false;
                }
            }
        } catch (const winrt::hresult_error&) {
            // An app closing mid-read; the next reading will be fine.
            manager = nullptr;
        }
        Snapshot snap = MediaSnapshot(np, int64_t(std::time(nullptr)));

        lock.lock();
        latest_ = std::move(snap);
        fresh_ = true;
        lock.unlock();
        wake_();
        lock.lock();
        // Read again in a second, or at once for a command; and shortly
        // after a command, since apps take a moment to update.
        cv_.wait_for(lock, std::chrono::milliseconds(command != MediaCommand::None ? 250 : 1000),
                     [&] { return stop_ || command_ != MediaCommand::None; });
    }
    lock.unlock();
    manager = nullptr;
    winrt::uninit_apartment();
}

}  // namespace ks
