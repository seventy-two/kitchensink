#include "images.h"

#include <wincodec.h>
#include <wrl/client.h>

#include <algorithm>

#include "net.h"

using Microsoft::WRL::ComPtr;

namespace ks {

namespace {

// Thumbnails are scaled down to at most this wide when decoded.
constexpr UINT kMaxWidth = 480;
constexpr size_t kMaxEntries = 200;

bool decode(IWICImagingFactory* wic, const std::string& bytes, UINT& w, UINT& h, std::vector<unsigned char>& rgba) {
    ComPtr<IWICStream> stream;
    ComPtr<IWICBitmapDecoder> decoder;
    ComPtr<IWICBitmapFrameDecode> frame;
    if (FAILED(wic->CreateStream(&stream)) ||
        FAILED(stream->InitializeFromMemory((BYTE*)bytes.data(), DWORD(bytes.size()))) ||
        FAILED(wic->CreateDecoderFromStream(stream.Get(), nullptr, WICDecodeMetadataCacheOnDemand, &decoder)) ||
        FAILED(decoder->GetFrame(0, &frame)) || FAILED(frame->GetSize(&w, &h)) || w == 0 || h == 0)
        return false;

    ComPtr<IWICBitmapSource> source = frame;
    if (w > kMaxWidth) {
        ComPtr<IWICBitmapScaler> scaler;
        UINT sh = std::max<UINT>(1, h * kMaxWidth / w);
        if (FAILED(wic->CreateBitmapScaler(&scaler)) ||
            FAILED(scaler->Initialize(frame.Get(), kMaxWidth, sh, WICBitmapInterpolationModeFant)))
            return false;
        source = scaler;
        w = kMaxWidth;
        h = sh;
    }
    ComPtr<IWICFormatConverter> conv;
    if (FAILED(wic->CreateFormatConverter(&conv)) ||
        FAILED(conv->Initialize(source.Get(), GUID_WICPixelFormat32bppRGBA, WICBitmapDitherTypeNone, nullptr, 0,
                                WICBitmapPaletteTypeCustom)))
        return false;
    rgba.resize(size_t(w) * h * 4);
    return SUCCEEDED(conv->CopyPixels(nullptr, w * 4, UINT(rgba.size()), rgba.data()));
}

}  // namespace

ImageCache::ImageCache(ID3D11Device* device, std::function<void()> wake)
    : device_(device), wake_(std::move(wake)), thread_(&ImageCache::run, this) {}

ImageCache::~ImageCache() {
    {
        std::lock_guard<std::mutex> lock(mu_);
        stop_ = true;
    }
    cv_.notify_all();
    thread_.join();
    for (auto& [url, e] : entries_)
        if (e.tex.srv) e.tex.srv->Release();
}

const Texture* ImageCache::Get(const std::string& url) {
    if (url.empty()) return nullptr;
    auto it = entries_.find(url);
    if (it == entries_.end()) {
        evict();
        entries_[url].lastUsed = ++tick_;
        {
            std::lock_guard<std::mutex> lock(mu_);
            queue_.push_back(url);
        }
        cv_.notify_one();
        return nullptr;
    }
    it->second.lastUsed = ++tick_;
    return it->second.tex.srv ? &it->second.tex : nullptr;
}

void ImageCache::evict() {
    while (entries_.size() >= kMaxEntries) {
        auto oldest = std::min_element(entries_.begin(), entries_.end(), [](const auto& a, const auto& b) {
            // Never evict one still loading; its result would be lost.
            if (a.second.done != b.second.done) return a.second.done;
            return a.second.lastUsed < b.second.lastUsed;
        });
        if (!oldest->second.done) return;
        if (oldest->second.tex.srv) oldest->second.tex.srv->Release();
        entries_.erase(oldest);
    }
}

void ImageCache::Upload() {
    std::vector<Decoded> ready;
    {
        std::lock_guard<std::mutex> lock(mu_);
        ready.swap(decoded_);
    }
    for (auto& d : ready) {
        auto it = entries_.find(d.url);
        if (it == entries_.end()) continue;
        it->second.done = true;
        if (d.rgba.empty()) continue;

        D3D11_TEXTURE2D_DESC desc{};
        desc.Width = d.width;
        desc.Height = d.height;
        desc.MipLevels = 1;
        desc.ArraySize = 1;
        desc.Format = DXGI_FORMAT_R8G8B8A8_UNORM;
        desc.SampleDesc.Count = 1;
        desc.Usage = D3D11_USAGE_IMMUTABLE;
        desc.BindFlags = D3D11_BIND_SHADER_RESOURCE;
        D3D11_SUBRESOURCE_DATA data{d.rgba.data(), UINT(d.width * 4), 0};
        ComPtr<ID3D11Texture2D> tex;
        if (FAILED(device_->CreateTexture2D(&desc, &data, &tex))) continue;
        ID3D11ShaderResourceView* srv = nullptr;
        if (FAILED(device_->CreateShaderResourceView(tex.Get(), nullptr, &srv))) continue;
        it->second.tex = {srv, d.width, d.height};
    }
}

void ImageCache::run() {
    CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    {
        ComPtr<IWICImagingFactory> wic;
        CoCreateInstance(CLSID_WICImagingFactory, nullptr, CLSCTX_INPROC_SERVER, IID_PPV_ARGS(&wic));
        Http http;
        for (;;) {
            std::string url;
            {
                std::unique_lock<std::mutex> lock(mu_);
                cv_.wait(lock, [&] { return stop_ || !queue_.empty(); });
                if (stop_) break;
                url = std::move(queue_.front());
                queue_.pop_front();
            }
            Decoded d{url};
            HttpResult r = http.Request("GET", url, "", 8 << 20);
            UINT w = 0, h = 0;
            if (wic && r.ok() && decode(wic.Get(), r.body, w, h, d.rgba)) {
                d.width = int(w);
                d.height = int(h);
            } else {
                d.rgba.clear();
            }
            {
                std::lock_guard<std::mutex> lock(mu_);
                decoded_.push_back(std::move(d));
            }
            wake_();
        }
    }
    CoUninitialize();
}

}  // namespace ks
