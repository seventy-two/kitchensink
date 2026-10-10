// A parser for server-sent events, fed the response body in whatever chunks
// the network delivers.
#pragma once

#include <string>
#include <string_view>
#include <vector>

namespace ks {

struct SseMessage {
    std::string id;     // the last ID the stream set, if any
    std::string event;  // "message" if the stream didn't name it
    std::string data;
};

class SseParser {
public:
    // Feed adds bytes from the stream and appends any messages they
    // complete to out. Comment lines (heartbeats) produce nothing.
    void Feed(std::string_view chunk, std::vector<SseMessage>& out);

private:
    void line(std::string_view l, std::vector<SseMessage>& out);

    std::string buf_;
    std::string lastID_, event_, data_;
    bool hasData_ = false;
    bool skipLF_ = false;
};

}  // namespace ks
