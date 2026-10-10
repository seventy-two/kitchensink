#include "sse.h"

namespace ks {

void SseParser::Feed(std::string_view chunk, std::vector<SseMessage>& out) {
    // Lines end in \n, \r\n or \r. A \r\n can be split across chunks, so
    // after a \r the next \n is skipped even if it's in the next chunk.
    for (char c : chunk) {
        if (skipLF_) {
            skipLF_ = false;
            if (c == '\n') continue;
        }
        if (c == '\r' || c == '\n') {
            line(buf_, out);
            buf_.clear();
            skipLF_ = c == '\r';
            continue;
        }
        buf_ += c;
    }
}

void SseParser::line(std::string_view l, std::vector<SseMessage>& out) {
    if (l.empty()) {
        if (hasData_) out.push_back({lastID_, event_.empty() ? "message" : event_, data_});
        event_.clear();
        data_.clear();
        hasData_ = false;
        return;
    }
    if (l[0] == ':') return;

    std::string_view field = l, value;
    if (auto colon = l.find(':'); colon != std::string_view::npos) {
        field = l.substr(0, colon);
        value = l.substr(colon + 1);
        if (!value.empty() && value[0] == ' ') value.remove_prefix(1);
    }
    if (field == "data") {
        if (hasData_) data_ += '\n';
        data_ += value;
        hasData_ = true;
    } else if (field == "event") {
        event_ = value;
    } else if (field == "id") {
        lastID_ = value;
    }
    // "retry" and unknown fields are ignored; we manage reconnects.
}

}  // namespace ks
