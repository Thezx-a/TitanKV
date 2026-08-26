#pragma once

#include <memory>
#include <string>

#include "core/block_cache.h"
#include "core/sstable_reader.h"
#include "utils/lru_cache.h"
#include "utils/metrics.h"

namespace minikv {
namespace core {

// LRU of open SSTableReaders keyed by path (T2.3).
// Avoids re-open + re-parse index on every Get/Iterator/compaction probe.
class TableCache {
public:
    static constexpr size_t kDefaultCapacity = 128;

    explicit TableCache(size_t capacity = kDefaultCapacity,
                        BlockCache* block_cache = nullptr)
        : cache_(capacity == 0 ? 1 : capacity), block_cache_(block_cache) {}

    // Returns nullptr if open fails.
    std::shared_ptr<SSTableReader> get(const std::string& path) {
        if (auto hit = cache_.get(path)) {
            utils::EngineMetrics::instance().table_cache_hits.fetch_add(1, std::memory_order_relaxed);
            return *hit;
        }
        utils::EngineMetrics::instance().table_cache_misses.fetch_add(1, std::memory_order_relaxed);
        auto opened = SSTableReader::open(path, block_cache_);
        if (!opened) return nullptr;
        std::shared_ptr<SSTableReader> shared(std::move(opened));
        cache_.put(path, shared);
        return shared;
    }

    void evict(const std::string& path) {
        cache_.erase(path);
        if (block_cache_) block_cache_->invalidatePath(path);
    }

    size_t size() const { return cache_.size(); }

private:
    utils::LRUCache<std::string, std::shared_ptr<SSTableReader>> cache_;
    BlockCache* block_cache_;
};

}  // namespace core
}  // namespace minikv
