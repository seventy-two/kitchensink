// Panels about this PC itself (CPU, memory, GPU and drives), drawn like any
// monitor's. The numbers come from sysinfo.cpp; turning them into panels is
// kept here, free of Windows, so it's unit tested on any platform.
#pragma once

#include <cstdint>
#include <deque>
#include <string>
#include <utility>
#include <vector>

#include "model.h"

namespace ks {

// Local panels have this kind, and are never refreshed from the server.
inline constexpr const char* kLocalKind = "local";

inline bool IsLocal(const Snapshot& s) { return s.kind == kLocalKind; }

// PcSample is one reading. Negative percentages and zero totals mean the
// value couldn't be read.
struct PcSample {
    float cpu = -1, gpu = -1;
    uint64_t ramUsed = 0, ramTotal = 0;
    uint64_t vramUsed = 0, vramTotal = 0;
    std::string gpuName;
};

struct DriveInfo {
    std::string root;  // "C:\"
    std::string label;
    uint64_t total = 0, free = 0;
};

// PcHistory keeps recent samples for the sparklines.
class PcHistory {
public:
    static constexpr size_t kLength = 60;  // a minute at one a second
    void Add(PcSample s);
    // Build makes the "This PC" panel as of now (Unix seconds).
    Snapshot Build(int64_t now) const;

private:
    std::deque<PcSample> samples_;
};

// DrivesSnapshot is the "Drives" panel.
Snapshot DrivesSnapshot(const std::vector<DriveInfo>& drives, int64_t now);

// GpuUtilisation turns the "GPU Engine" performance counters, one per
// process and engine, into a percentage as Task Manager shows it: each
// engine's processes added up, then the busiest engine. Instances are
// named like "pid_1234_luid_0x0_0xD1A5_phys_0_eng_0_engtype_3D".
float GpuUtilisation(const std::vector<std::pair<std::string, double>>& instances);

// FormatBytes writes a size in GB to one place, or MB under 1 GB.
std::string FormatBytes(uint64_t bytes);

// LoadTone is how worrying a percentage is: warn from 85, bad from 95.
Tone LoadTone(float pct);

}  // namespace ks
