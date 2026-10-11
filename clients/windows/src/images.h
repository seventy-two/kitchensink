// Thumbnails for list items: downloaded and decoded on a background thread,
// turned into textures on the UI thread.
#pragma once

#include <d3d11.h>

#include <condition_variable>
#include <deque>
#include <functional>
#include <mutex>
#include <string>
#include <thread>
#include <unordered_map>
#include <vector>

namespace ks {

struct Texture {
    ID3D11ShaderResourceView* srv = nullptr;
    int width = 0, height = 0;
};

class ImageCache {
public:
    // wake is called from the background thread when an image is ready.
    ImageCache(ID3D11Device* device, std::function<void()> wake);
    ~ImageCache();

    // Get returns the image's texture, or nullptr while it loads or if it
    // failed. The first call starts loading it.
    const Texture* Get(const std::string& url);

    // Upload turns decoded images into textures. Call it on the UI thread.
    void Upload();

    // Provide supplies an image's bytes for a key that isn't a URL, such as
    // album art read from Windows. Any thread may call it; call it before
    // the first Get of that key.
    void Provide(const std::string& key, std::string bytes);

private:
    struct Decoded {
        std::string url;
        int width = 0, height = 0;
        std::vector<unsigned char> rgba;  // empty if it failed
    };
    struct Entry {
        bool done = false;
        Texture tex;
        unsigned lastUsed = 0;
    };

    void run();
    void evict();

    ID3D11Device* device_;
    std::function<void()> wake_;
    std::unordered_map<std::string, Entry> entries_;
    unsigned tick_ = 0;

    std::mutex mu_;
    std::condition_variable cv_;
    std::deque<std::string> queue_;
    std::unordered_map<std::string, std::string> provided_;  // bytes by key, until decoded
    std::vector<Decoded> decoded_;
    bool stop_ = false;
    std::thread thread_;
};

}  // namespace ks
