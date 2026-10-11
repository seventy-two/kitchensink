#include "sysinfo.h"

#include <windows.h>
#include <dxgi1_4.h>
#include <pdh.h>
#include <pdhmsg.h>

#include <algorithm>
#include <ctime>
#include <string>

#include "local.h"
#include "net.h"

namespace ks {

namespace {

// readCounter reads every value of a wildcard performance counter.
std::vector<std::pair<std::string, double>> readCounter(PDH_HCOUNTER counter) {
    std::vector<std::pair<std::string, double>> out;
    DWORD size = 0, count = 0;
    if (PdhGetFormattedCounterArrayW(counter, PDH_FMT_DOUBLE | PDH_FMT_NOCAP100, &size, &count, nullptr) !=
        PDH_MORE_DATA)
        return out;
    std::vector<unsigned char> buf(size);
    auto* items = reinterpret_cast<PDH_FMT_COUNTERVALUE_ITEM_W*>(buf.data());
    if (PdhGetFormattedCounterArrayW(counter, PDH_FMT_DOUBLE | PDH_FMT_NOCAP100, &size, &count, items) !=
        ERROR_SUCCESS)
        return out;
    for (DWORD i = 0; i < count; i++) {
        if (items[i].FmtValue.CStatus != PDH_CSTATUS_VALID_DATA && items[i].FmtValue.CStatus != PDH_CSTATUS_NEW_DATA)
            continue;
        out.emplace_back(Narrow(items[i].szName), items[i].FmtValue.doubleValue);
    }
    return out;
}

// luidName is how the GPU counters name an adapter.
std::string luidName(LUID luid) {
    char buf[64];
    snprintf(buf, sizeof(buf), "luid_0x%08lX_0x%08lX", luid.HighPart, luid.LowPart);
    return buf;
}

// Gpu is the PC's main graphics card: the one with the most memory.
struct Gpu {
    std::string name, luid;
    uint64_t memory = 0;
};

Gpu findGpu() {
    Gpu best;
    IDXGIFactory1* factory = nullptr;
    if (FAILED(CreateDXGIFactory1(IID_PPV_ARGS(&factory)))) return best;
    IDXGIAdapter1* adapter = nullptr;
    for (UINT i = 0; factory->EnumAdapters1(i, &adapter) != DXGI_ERROR_NOT_FOUND; i++) {
        DXGI_ADAPTER_DESC1 d;
        if (SUCCEEDED(adapter->GetDesc1(&d)) && !(d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE) &&
            d.DedicatedVideoMemory > best.memory) {
            best = {Narrow(d.Description), luidName(d.AdapterLuid), d.DedicatedVideoMemory};
        }
        adapter->Release();
    }
    factory->Release();
    return best;
}

std::vector<DriveInfo> readDrives() {
    std::vector<DriveInfo> out;
    wchar_t roots[512];
    DWORD n = GetLogicalDriveStringsW(ARRAYSIZE(roots), roots);
    if (n == 0 || n > ARRAYSIZE(roots)) return out;
    for (const wchar_t* r = roots; *r; r += wcslen(r) + 1) {
        // Fixed drives only: network drives can stall, and removable ones
        // come and go.
        if (GetDriveTypeW(r) != DRIVE_FIXED) continue;
        ULARGE_INTEGER avail, total, free;
        if (!GetDiskFreeSpaceExW(r, &avail, &total, &free)) continue;
        wchar_t label[MAX_PATH + 1] = {};
        GetVolumeInformationW(r, label, ARRAYSIZE(label), nullptr, nullptr, nullptr, nullptr, 0);
        out.push_back({Narrow(r), Narrow(label), total.QuadPart, free.QuadPart});
    }
    return out;
}

}  // namespace

LocalStats::LocalStats(std::function<void()> wake) : wake_(std::move(wake)) {
    thread_ = std::thread([this] { run(); });
}

LocalStats::~LocalStats() {
    {
        std::lock_guard<std::mutex> lock(mu_);
        stop_ = true;
    }
    cv_.notify_all();
    thread_.join();
}

void LocalStats::Want() {
    unsigned long long before = wanted_.exchange(GetTickCount64());
    // Wake the thread if it had gone idle. Taking the lock first means it
    // can't miss this between checking and waiting.
    if (GetTickCount64() - before > 3000) {
        { std::lock_guard<std::mutex> lock(mu_); }
        cv_.notify_all();
    }
}

bool LocalStats::Take(std::vector<Snapshot>& out) {
    std::lock_guard<std::mutex> lock(mu_);
    if (!fresh_) return false;
    out = latest_;
    fresh_ = false;
    return true;
}

void LocalStats::run() {
    // Counters are added by their English names so they work whatever the
    // Windows display language. A missing one (e.g. no GPU counters on an
    // old driver) just reads as unknown.
    PDH_HQUERY query = nullptr;
    PDH_HCOUNTER cpu = nullptr, gpu = nullptr, vram = nullptr;
    if (PdhOpenQueryW(nullptr, 0, &query) == ERROR_SUCCESS) {
        // Processor Utility is what Task Manager shows; it can pass 100%
        // when the CPU boosts, so it's capped.
        if (PdhAddEnglishCounterW(query, L"\\Processor Information(_Total)\\% Processor Utility", 0, &cpu) !=
            ERROR_SUCCESS)
            PdhAddEnglishCounterW(query, L"\\Processor(_Total)\\% Processor Time", 0, &cpu);
        PdhAddEnglishCounterW(query, L"\\GPU Engine(*)\\Utilization Percentage", 0, &gpu);
        PdhAddEnglishCounterW(query, L"\\GPU Adapter Memory(*)\\Dedicated Usage", 0, &vram);
        PdhCollectQueryData(query);
    }
    Gpu card = findGpu();
    PcHistory history;
    std::vector<DriveInfo> drives;
    unsigned long long drivesAt = 0;

    std::unique_lock<std::mutex> lock(mu_);
    while (!stop_) {
        bool wanted = GetTickCount64() - wanted_.load() < 3000;
        if (!wanted) {
            // Idle until Want wakes us; the gap would spoil the sparklines.
            history = PcHistory();
            cv_.wait(lock, [&] { return stop_ || GetTickCount64() - wanted_.load() < 3000; });
            if (query) PdhCollectQueryData(query);  // a fresh baseline for rates
            cv_.wait_for(lock, std::chrono::seconds(1), [&] { return stop_; });
            continue;
        }
        lock.unlock();

        PcSample s;
        s.gpuName = card.name;
        s.vramTotal = card.memory;
        if (query && PdhCollectQueryData(query) == ERROR_SUCCESS) {
            PDH_FMT_COUNTERVALUE v;
            if (cpu && PdhGetFormattedCounterValue(cpu, PDH_FMT_DOUBLE | PDH_FMT_NOCAP100, nullptr, &v) ==
                           ERROR_SUCCESS)
                s.cpu = float(std::min(v.doubleValue, 100.0));
            if (gpu) s.gpu = GpuUtilisation(readCounter(gpu));
            if (vram) {
                // The card's own memory; the busiest adapter if it isn't
                // found by name.
                double used = 0, any = 0;
                for (const auto& [name, value] : readCounter(vram)) {
                    if (!card.luid.empty() && name.rfind(card.luid, 0) == 0) used += value;
                    any = std::max(any, value);
                }
                s.vramUsed = uint64_t(used > 0 ? used : any);
            }
        }
        MEMORYSTATUSEX mem{sizeof(mem)};
        if (GlobalMemoryStatusEx(&mem)) {
            s.ramTotal = mem.ullTotalPhys;
            s.ramUsed = mem.ullTotalPhys - mem.ullAvailPhys;
        }
        history.Add(std::move(s));
        // Drives change slowly.
        if (GetTickCount64() - drivesAt > 30000) {
            drives = readDrives();
            drivesAt = GetTickCount64();
        }
        int64_t now = int64_t(std::time(nullptr));
        std::vector<Snapshot> panels = {history.Build(now), DrivesSnapshot(drives, now)};

        lock.lock();
        latest_ = std::move(panels);
        fresh_ = true;
        lock.unlock();
        wake_();
        lock.lock();
        cv_.wait_for(lock, std::chrono::seconds(1), [&] { return stop_; });
    }
    lock.unlock();
    if (query) PdhCloseQuery(query);
}

}  // namespace ks
