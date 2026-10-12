// The dashboard: state from the stream, drawn with ImGui.
#pragma once

#include <windows.h>
#include <d3d11.h>

#include <array>
#include <functional>
#include <memory>
#include <string>
#include <unordered_map>
#include <vector>

#include "config.h"
#include "images.h"
#include "imgui_internal.h"
#include "model.h"
#include "net.h"
#include "media.h"
#include "sysinfo.h"
#include "theme.h"

namespace ks {

class App {
public:
    // wake is called from background threads when there's something new.
    // notify shows a Windows notification. The header doubles as hwnd's
    // title bar, with its own window buttons.
    App(HWND hwnd, ID3D11Device* device, std::function<void()> wake,
        std::function<void(const std::string& title, const std::string& body)> notify);
    ~App();

    // SetScale reloads fonts and style for a DPI scale (1 at 96 DPI).
    void SetScale(float scale);
    void SetDark(bool dark);

    // Pump takes in what the background threads have received. It runs
    // whether or not the window is showing, so notifications still fire.
    void Pump();
    // Frame draws the UI. Call it between ImGui::NewFrame and Render.
    void Frame();

    // NotificationClicked opens what the last notification was about. It
    // returns false if there was nothing to open, so the caller can show
    // the window instead.
    bool NotificationClicked();

    // FlushIn is how many milliseconds until held notifications are due,
    // or INFINITE if none are waiting.
    unsigned long FlushIn() const;

    // InCaption reports whether a point in client pixels is on the title
    // bar but not on one of its buttons, so dragging there moves the window.
    bool InCaption(float x, float y) const;

    // Busy reports whether the UI needs frames drawn continuously, e.g.
    // while typing.
    bool Busy() const { return busy_; }
    // Animating says whether something on screen is moving and wants steady
    // frames: the dancer.
    bool Animating() const { return animating_; }

private:
    void header();
    float windowButtons();
    // panels lists everything to draw, in the order the user dragged them
    // into; by default This PC, Now playing (while something is), the
    // server's monitors in config order, then Drives.
    std::vector<Snapshot> panels() const;
    void cards(float width);
    void card(const Snapshot& s, float width);
    void compactCards(float width);
    float compactCard(const Snapshot& s, float width, Show show, float body, bool* clicked);
    void summary(const std::vector<Stat>& stats, float width);
    void chips(const std::vector<Stat>& stats, float width);
    void refreshButton(const Snapshot& s);
    bool grip(const Snapshot& s, float y, bool shown);
    bool hoveringCard() const;
    void dragDrop();
    // block draws one block of a monitor's panel; monitor is where a
    // checklist sends its actions.
    void block(const std::string& monitor, const Block& b);
    void stats(const Block& b);
    void table(const Block& b);
    void list(const Block& b);
    void checklist(const std::string& monitor, const Block& b);
    void mediaBlock(const Block& b);
    void feed();
    void dancer(float width);
    void empty(const char* message, bool settingsButton);
    void settings();
    void openSettings();

    HWND hwnd_;
    float captionH_ = 0;
    std::vector<ImRect> noDrag_;  // title bar buttons, from the last frame
    int hoverButton_ = -1;        // window button under the mouse, for its tooltip
    double hoverSince_ = 0;

    float scale_ = 1;
    bool dark_ = true;
    // Compact shows each monitor's summary instead of its full panel, for
    // small windows; see UseCompact.
    bool compact_ = false;
    bool fontsDirty_ = true;
    Fonts fonts_;

    Config config_;
    Model model_;
    std::unique_ptr<Client> client_;
    std::unique_ptr<ImageCache> images_;
    std::unique_ptr<LocalStats> local_;
    std::vector<Snapshot> localPanels_;  // This PC, then Drives
    std::unique_ptr<MediaWatcher> media_;
    Snapshot mediaPanel_;  // no blocks while nothing's playing
    std::function<void(const std::string&, const std::string&)> notify_;
    std::string lastNotifyURL_;
    // Notifications are held briefly so a burst becomes one notification.
    std::vector<Event> pending_;
    unsigned long long pendingSince_ = 0;
    std::unordered_map<std::string, float> heights_;  // card heights last frame
    std::unordered_map<std::string, double> refreshing_;
    bool busy_ = false;
    bool animating_ = false, dancerShown_ = false;

    // Checklist ticks shown before the server confirms them, by
    // "monitor/item", with when they were made; a snapshot replaces them.
    std::unordered_map<std::string, std::pair<bool, double>> ticked_;
    std::unordered_map<std::string, std::array<char, 512>> newItem_;  // add boxes, by monitor

    // Dragging panels into a new order: the one being dragged, and where
    // each card was drawn this frame.
    std::string dragging_;
    std::vector<std::pair<std::string, ImRect>> cardRects_;

    bool settingsOpen_ = false;
    char urlBuf_[512] = {};
    char tokenBuf_[256] = {};
    int priorityChoice_ = 1;
    bool notifyChoice_ = true;
    bool startChoice_ = false;
    OledCare oledChoice_;
    bool dancerChoice_ = false;

    // OLED care: when the mouse was last over the window, and until when
    // something new keeps it bright (Unix seconds).
    int64_t lastActivity_ = 0;
    int64_t wakeUntil_ = 0;
    float captionTop_ = 0;  // where the title bar starts, as the orbit moves it
};

// OpenURL opens an http or https URL in the default browser. Other schemes
// are ignored, since URLs come from the server.
void OpenURL(const std::string& url);

}  // namespace ks
