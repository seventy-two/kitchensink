// HTTP over WinHTTP, and the client that keeps the event stream open.
#pragma once

#include <windows.h>
#include <winhttp.h>

#include <atomic>
#include <condition_variable>
#include <functional>
#include <mutex>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

#include "model.h"

namespace ks {

std::wstring Widen(std::string_view s);
std::string Narrow(std::wstring_view s);

struct HttpResult {
    DWORD status = 0;  // 0 if the request failed before a response
    std::string body;
    std::string error;
    bool ok() const { return status >= 200 && status < 300; }
};

// Http is a WinHTTP session. Requests are synchronous and safe to make from
// several threads at once.
class Http {
public:
    Http();
    ~Http();
    Http(const Http&) = delete;
    Http& operator=(const Http&) = delete;

    // Request makes a request and reads the whole response, up to maxBytes.
    // A non-empty body is sent as JSON.
    HttpResult Request(const std::string& method, const std::string& url, const std::string& token,
                       size_t maxBytes = 16 << 20, const std::string& body = {});

    // Stream makes a GET and passes each chunk of the body to onData until
    // it returns false, the connection ends, or Cancel is called. It
    // returns why the stream ended. A read that waits longer than
    // receiveTimeoutMs fails, so a dead connection is noticed.
    std::string Stream(const std::string& url, const std::string& token, DWORD receiveTimeoutMs,
                       const std::function<bool(std::string_view)>& onData, DWORD* status);

    // Cancel ends a Stream in progress on another thread.
    void Cancel();

private:
    HINTERNET open(const std::string& method, const std::string& url, const std::string& token,
                   std::string& error, const std::string& body = {});

    HINTERNET session_ = nullptr;
    std::mutex mu_;
    HINTERNET streaming_ = nullptr;
};

// Inbound is something the stream client received, for the UI thread.
struct Inbound {
    enum class Type { SyncStart, Snapshot, Event, Ready };
    Type type;
    Snapshot snapshot;
    Event event;
    // Replayed is true for events sent before the stream was ready: the
    // history on first connect, or what was missed while disconnected.
    bool replayed = false;
    // History is true for events from before the app started.
    bool history = false;
};

enum class ConnState { NotConfigured, Connecting, Connected, Offline };

// Client keeps the server's event stream open on a background thread,
// reconnecting with backoff and catching up on missed events.
class Client {
public:
    // wake is called from the background thread whenever there's
    // something to Drain.
    explicit Client(std::function<void()> wake);
    ~Client();

    // Start connects to the server, replacing any earlier connection.
    void Start(const std::string& baseURL, const std::string& token);
    void Stop();

    // Drain moves everything received since the last call into out.
    void Drain(std::vector<Inbound>& out);

    ConnState State() const { return state_; }
    std::string LastError();

    // Refresh asks the server to poll a monitor now, in the background.
    void Refresh(const std::string& monitor);
    // Act sends a monitor an action, such as ticking a todo, in the
    // background. action is the JSON the API takes.
    void Act(const std::string& monitor, const std::string& action);

private:
    void run(std::string base, std::string token);
    void push(Inbound in);
    void setError(std::string e);
    bool sleepFor(int seconds);  // false if stopping

    std::function<void()> wake_;
    Http http_;
    std::thread thread_;
    std::atomic<bool> stop_{false};
    std::atomic<ConnState> state_{ConnState::NotConfigured};
    std::string base_, token_;

    std::mutex mu_;
    std::condition_variable cv_;
    std::vector<Inbound> inbox_;
    std::string error_;
};

}  // namespace ks
