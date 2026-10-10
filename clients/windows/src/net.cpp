#include "net.h"

#include <algorithm>

#include "sse.h"

namespace ks {

std::wstring Widen(std::string_view s) {
    if (s.empty()) return {};
    int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), int(s.size()), nullptr, 0);
    std::wstring w(n, L'\0');
    MultiByteToWideChar(CP_UTF8, 0, s.data(), int(s.size()), w.data(), n);
    return w;
}

std::string Narrow(std::wstring_view w) {
    if (w.empty()) return {};
    int n = WideCharToMultiByte(CP_UTF8, 0, w.data(), int(w.size()), nullptr, 0, nullptr, nullptr);
    std::string s(n, '\0');
    WideCharToMultiByte(CP_UTF8, 0, w.data(), int(w.size()), s.data(), n, nullptr, nullptr);
    return s;
}

namespace {

// winError describes a WinHTTP or system error code.
std::string winError(DWORD code) {
    switch (code) {
    case ERROR_WINHTTP_TIMEOUT: return "timed out";
    case ERROR_WINHTTP_NAME_NOT_RESOLVED: return "server name not found";
    case ERROR_WINHTTP_CANNOT_CONNECT: return "couldn't connect";
    case ERROR_WINHTTP_CONNECTION_ERROR: return "connection lost";
    case ERROR_WINHTTP_OPERATION_CANCELLED: return "cancelled";
    case ERROR_WINHTTP_SECURE_FAILURE: return "secure connection failed";
    case ERROR_WINHTTP_UNRECOGNIZED_SCHEME: return "URL must start with http:// or https://";
    case ERROR_WINHTTP_INVALID_URL: return "invalid URL";
    }
    wchar_t* msg = nullptr;
    HMODULE mod = GetModuleHandleW(L"winhttp.dll");
    DWORD n = FormatMessageW(FORMAT_MESSAGE_ALLOCATE_BUFFER | FORMAT_MESSAGE_FROM_HMODULE | FORMAT_MESSAGE_FROM_SYSTEM |
                                 FORMAT_MESSAGE_IGNORE_INSERTS,
                             mod, code, 0, reinterpret_cast<wchar_t*>(&msg), 0, nullptr);
    std::string s = n ? Narrow(std::wstring_view(msg, n)) : "error " + std::to_string(code);
    if (msg) LocalFree(msg);
    while (!s.empty() && (s.back() == '\n' || s.back() == '\r' || s.back() == '.')) s.pop_back();
    return s;
}

DWORD statusOf(HINTERNET req) {
    DWORD status = 0, size = sizeof(status);
    WinHttpQueryHeaders(req, WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER, WINHTTP_HEADER_NAME_BY_INDEX,
                        &status, &size, WINHTTP_NO_HEADER_INDEX);
    return status;
}

// Handles closes a request and its connection together.
struct Handles {
    HINTERNET connect = nullptr, request = nullptr;
    ~Handles() {
        if (request) WinHttpCloseHandle(request);
        if (connect) WinHttpCloseHandle(connect);
    }
};

}  // namespace

Http::Http() {
    session_ = WinHttpOpen(L"kitchensink-windows/1.0", WINHTTP_ACCESS_TYPE_AUTOMATIC_PROXY, WINHTTP_NO_PROXY_NAME,
                           WINHTTP_NO_PROXY_BYPASS, 0);
    if (session_) {
        WinHttpSetTimeouts(session_, 10000, 10000, 15000, 30000);
        DWORD protocols = WINHTTP_FLAG_SECURE_PROTOCOL_TLS1_2 | WINHTTP_FLAG_SECURE_PROTOCOL_TLS1_3;
        WinHttpSetOption(session_, WINHTTP_OPTION_SECURE_PROTOCOLS, &protocols, sizeof(protocols));
        DWORD decompress = WINHTTP_DECOMPRESSION_FLAG_ALL;
        WinHttpSetOption(session_, WINHTTP_OPTION_DECOMPRESSION, &decompress, sizeof(decompress));
    }
}

Http::~Http() {
    if (session_) WinHttpCloseHandle(session_);
}

// open connects and sends a request. The connection handle is stored as
// the request's context so it's closed with it; see closeRequest.
HINTERNET Http::open(const std::string& method, const std::string& url, const std::string& token, std::string& error) {
    if (!session_) {
        error = "WinHTTP unavailable";
        return nullptr;
    }
    std::wstring wurl = Widen(url);
    URL_COMPONENTS uc{sizeof(uc)};
    wchar_t host[256], path[2048];
    uc.lpszHostName = host;
    uc.dwHostNameLength = ARRAYSIZE(host);
    uc.lpszUrlPath = path;
    uc.dwUrlPathLength = ARRAYSIZE(path);
    uc.dwExtraInfoLength = 1;  // ask for the query; it's left joined to the path
    if (!WinHttpCrackUrl(wurl.c_str(), 0, 0, &uc)) {
        error = winError(GetLastError());
        return nullptr;
    }
    std::wstring target(uc.lpszUrlPath, uc.dwUrlPathLength);
    if (uc.lpszExtraInfo) target.append(uc.lpszExtraInfo, uc.dwExtraInfoLength);
    if (target.empty()) target = L"/";

    HINTERNET conn = WinHttpConnect(session_, host, uc.nPort, 0);
    if (!conn) {
        error = winError(GetLastError());
        return nullptr;
    }
    DWORD flags = uc.nScheme == INTERNET_SCHEME_HTTPS ? WINHTTP_FLAG_SECURE : 0;
    HINTERNET req = WinHttpOpenRequest(conn, Widen(method).c_str(), target.c_str(), nullptr, WINHTTP_NO_REFERER,
                                       WINHTTP_DEFAULT_ACCEPT_TYPES, flags);
    if (!req) {
        error = winError(GetLastError());
        WinHttpCloseHandle(conn);
        return nullptr;
    }
    WinHttpSetOption(req, WINHTTP_OPTION_CONTEXT_VALUE, &conn, sizeof(conn));

    std::wstring headers;
    if (!token.empty()) headers = L"Authorization: Bearer " + Widen(token) + L"\r\n";
    if (!WinHttpSendRequest(req, headers.empty() ? WINHTTP_NO_ADDITIONAL_HEADERS : headers.c_str(), DWORD(-1),
                            WINHTTP_NO_REQUEST_DATA, 0, 0, 0) ||
        !WinHttpReceiveResponse(req, nullptr)) {
        error = winError(GetLastError());
        WinHttpCloseHandle(req);
        WinHttpCloseHandle(conn);
        return nullptr;
    }
    return req;
}

static void closeRequest(HINTERNET req) {
    HINTERNET conn = nullptr;
    DWORD size = sizeof(conn);
    WinHttpQueryOption(req, WINHTTP_OPTION_CONTEXT_VALUE, &conn, &size);
    WinHttpCloseHandle(req);
    if (conn) WinHttpCloseHandle(conn);
}

HttpResult Http::Request(const std::string& method, const std::string& url, const std::string& token,
                         size_t maxBytes) {
    HttpResult r;
    HINTERNET req = open(method, url, token, r.error);
    if (!req) return r;
    r.status = statusOf(req);
    char buf[16384];
    for (;;) {
        DWORD n = 0;
        if (!WinHttpReadData(req, buf, sizeof(buf), &n)) {
            r.error = winError(GetLastError());
            break;
        }
        if (n == 0) break;
        if (r.body.size() + n > maxBytes) {
            r.error = "response too large";
            break;
        }
        r.body.append(buf, n);
    }
    closeRequest(req);
    if (r.error.empty() && !r.ok()) r.error = "server returned " + std::to_string(r.status);
    return r;
}

std::string Http::Stream(const std::string& url, const std::string& token, DWORD receiveTimeoutMs,
                         const std::function<bool(std::string_view)>& onData, DWORD* status) {
    std::string error;
    *status = 0;
    HINTERNET req = open("GET", url, token, error);
    if (!req) return error;
    *status = statusOf(req);
    if (*status != 200) {
        closeRequest(req);
        if (*status == 401) return "the server rejected the token";
        return "server returned " + std::to_string(*status);
    }
    WinHttpSetOption(req, WINHTTP_OPTION_RECEIVE_TIMEOUT, &receiveTimeoutMs, sizeof(receiveTimeoutMs));
    {
        std::lock_guard<std::mutex> lock(mu_);
        streaming_ = req;
    }
    char buf[16384];
    for (;;) {
        // A synchronous read waits to fill its whole buffer, so ask what's
        // arrived first; that wait returns as soon as anything does.
        DWORD avail = 0, n = 0;
        if (!WinHttpQueryDataAvailable(req, &avail) ||
            (avail > 0 && !WinHttpReadData(req, buf, std::min<DWORD>(avail, sizeof(buf)), &n))) {
            error = winError(GetLastError());
            break;
        }
        if (avail == 0 || n == 0) {
            error = "the server closed the stream";
            break;
        }
        if (!onData(std::string_view(buf, n))) {
            error = "stopped";
            break;
        }
    }
    std::lock_guard<std::mutex> lock(mu_);
    // If Cancel got here first it has already closed the request.
    if (streaming_) closeRequest(req);
    streaming_ = nullptr;
    return error;
}

void Http::Cancel() {
    std::lock_guard<std::mutex> lock(mu_);
    if (streaming_) {
        // Closing the handle makes the blocked read fail straight away.
        closeRequest(streaming_);
        streaming_ = nullptr;
    }
}

Client::Client(std::function<void()> wake) : wake_(std::move(wake)) {}

Client::~Client() { Stop(); }

void Client::Start(const std::string& baseURL, const std::string& token) {
    Stop();
    std::string base = baseURL;
    while (!base.empty() && base.back() == '/') base.pop_back();
    base_ = base;
    token_ = token;
    if (base.empty() || token.empty()) {
        state_ = ConnState::NotConfigured;
        return;
    }
    stop_ = false;
    thread_ = std::thread(&Client::run, this, base, token);
}

void Client::Stop() {
    if (!thread_.joinable()) return;
    stop_ = true;
    cv_.notify_all();
    http_.Cancel();
    thread_.join();
    std::lock_guard<std::mutex> lock(mu_);
    inbox_.clear();
}

void Client::Drain(std::vector<Inbound>& out) {
    std::lock_guard<std::mutex> lock(mu_);
    for (auto& in : inbox_) out.push_back(std::move(in));
    inbox_.clear();
}

std::string Client::LastError() {
    std::lock_guard<std::mutex> lock(mu_);
    return error_;
}

void Client::push(Inbound in) {
    {
        std::lock_guard<std::mutex> lock(mu_);
        inbox_.push_back(std::move(in));
    }
    wake_();
}

void Client::setError(std::string e) {
    {
        std::lock_guard<std::mutex> lock(mu_);
        error_ = std::move(e);
    }
    wake_();
}

bool Client::sleepFor(int seconds) {
    std::unique_lock<std::mutex> lock(mu_);
    cv_.wait_for(lock, std::chrono::seconds(seconds), [&] { return stop_.load(); });
    return !stop_;
}

void Client::Refresh(const std::string& monitor) {
    std::string url = base_ + "/api/monitors/" + monitor + "/refresh", token = token_;
    std::thread([url, token] {
        Http h;
        h.Request("POST", url, token);
    }).detach();
}

void Client::run(std::string base, std::string token) {
    int64_t since = -1;  // -1 until the history has loaded
    int backoff = 1;
    while (!stop_) {
        state_ = ConnState::Connecting;
        wake_();

        if (since < 0) {
            HttpResult r = http_.Request("GET", base + "/api/events?limit=100", token);
            if (r.ok()) {
                try {
                    auto j = nlohmann::json::parse(r.body);
                    since = 0;
                    for (const auto& e : j.at("events")) {
                        Inbound in{Inbound::Type::Event};
                        in.event = ParseEvent(e);
                        in.replayed = in.history = true;
                        since = std::max(since, in.event.id);
                        push(std::move(in));
                    }
                } catch (const std::exception&) {
                    r.error = "the server sent something unexpected";
                    since = -1;
                }
            }
            if (since < 0) {
                state_ = ConnState::Offline;
                setError(r.status == 401 ? "the server rejected the token" : r.error);
                if (!sleepFor(r.status == 401 ? 60 : backoff)) return;
                backoff = std::min(backoff * 2, 60);
                continue;
            }
        }

        push({Inbound::Type::SyncStart});
        SseParser parser;
        std::vector<SseMessage> msgs;
        bool ready = false;
        auto onData = [&](std::string_view chunk) {
            msgs.clear();
            parser.Feed(chunk, msgs);
            for (const auto& m : msgs) {
                try {
                    auto j = nlohmann::json::parse(m.data);
                    if (m.event == "snapshot") {
                        Inbound in{Inbound::Type::Snapshot};
                        in.snapshot = ParseSnapshot(j);
                        push(std::move(in));
                    } else if (m.event == "event") {
                        Inbound in{Inbound::Type::Event};
                        in.event = ParseEvent(j);
                        in.replayed = !ready;
                        since = std::max(since, in.event.id);
                        push(std::move(in));
                    } else if (m.event == "ready") {
                        ready = true;
                        backoff = 1;
                        state_ = ConnState::Connected;
                        setError("");
                        push({Inbound::Type::Ready});
                    }
                } catch (const std::exception&) {
                    // Skip a message we can't read rather than drop the
                    // connection.
                }
            }
            return !stop_.load();
        };
        DWORD status = 0;
        // The server sends a heartbeat every 30 seconds, so 75 seconds of
        // silence means the connection has gone.
        std::string err = http_.Stream(base + "/api/stream?heartbeat=30&since=" + std::to_string(since), token,
                                       75000, onData, &status);
        if (stop_) return;
        state_ = ConnState::Offline;
        setError(err);
        if (!sleepFor(status == 401 ? 60 : backoff)) return;
        backoff = std::min(backoff * 2, 60);
    }
}

}  // namespace ks
