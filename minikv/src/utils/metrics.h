#pragma once
#include <atomic>
#include <cstdint>
#include <string>

namespace minikv {
namespace utils {

// Process-wide engine counters (T2.6 minimal). Not a full Prometheus client.
struct EngineMetrics {
    std::atomic<uint64_t> puts{0};
    std::atomic<uint64_t> gets{0};
    std::atomic<uint64_t> get_hits{0};
    std::atomic<uint64_t> get_misses{0};
    std::atomic<uint64_t> deletes{0};
    std::atomic<uint64_t> flushes{0};
    std::atomic<uint64_t> compactions{0};
    // E4: background compaction merge attempts that returned !ok (then retried).
    std::atomic<uint64_t> compaction_failures{0};
    std::atomic<uint64_t> write_stalls{0};
    std::atomic<uint64_t> table_cache_hits{0};
    std::atomic<uint64_t> table_cache_misses{0};
    // E3: decompressed SST block LRU (BlockCache), distinct from TableCache (open Reader).
    std::atomic<uint64_t> block_cache_hits{0};
    std::atomic<uint64_t> block_cache_misses{0};

    static EngineMetrics& instance() {
        static EngineMetrics m;
        return m;
    }

    std::string prometheusText() const;
};

// Bind a tiny HTTP listener serving GET /metrics and GET /healthz.
// Runs a background thread; safe to call once at process start.
// Returns false if bind fails.
bool startMetricsHttp(const std::string& host, int port);
void stopMetricsHttp();

}  // namespace utils
}  // namespace minikv
