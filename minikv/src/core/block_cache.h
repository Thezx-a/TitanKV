#pragma once
#include <cstdint>
#include <list>
#include <mutex>
#include <optional>
#include <string>
#include <unordered_map>

namespace minikv {
namespace core {

// Cache key: (sst_path, block_offset)
struct BlockCacheKey {
    std::string path;
    uint64_t offset = 0;

    bool operator==(const BlockCacheKey& o) const {
        return offset == o.offset && path == o.path;
    }
};

struct BlockCacheKeyHash {
    size_t operator()(const BlockCacheKey& k) const {
        return std::hash<std::string>{}(k.path) ^ (std::hash<uint64_t>{}(k.offset) << 1);
    }
};

// Byte-capacity LRU cache for decompressed SSTable blocks.
class BlockCache {
public:
    explicit BlockCache(size_t capacity_bytes) : capacity_bytes_(capacity_bytes) {}

    std::optional<std::string> get(const BlockCacheKey& key) {
        std::lock_guard<std::mutex> lock(mutex_);
        auto it = map_.find(key);
        if (it == map_.end()) return std::nullopt;
        list_.splice(list_.begin(), list_, it->second);
        return it->second->second.data;
    }

    void put(const BlockCacheKey& key, std::string data) {
        std::lock_guard<std::mutex> lock(mutex_);
        size_t sz = data.size();
        auto it = map_.find(key);
        if (it != map_.end()) {
            used_bytes_ -= it->second->second.data.size();
            list_.erase(it->second);
            map_.erase(it);
        }
        list_.push_front({key, Entry{std::move(data), sz}});
        map_[key] = list_.begin();
        used_bytes_ += sz;
        evict();
    }

    void invalidatePath(const std::string& path) {
        std::lock_guard<std::mutex> lock(mutex_);
        for (auto it = list_.begin(); it != list_.end();) {
            if (it->first.path == path) {
                used_bytes_ -= it->second.size;
                map_.erase(it->first);
                it = list_.erase(it);
            } else {
                ++it;
            }
        }
    }

    size_t usedBytes() const {
        std::lock_guard<std::mutex> lock(mutex_);
        return used_bytes_;
    }

    size_t entryCount() const {
        std::lock_guard<std::mutex> lock(mutex_);
        return map_.size();
    }

private:
    struct Entry {
        std::string data;
        size_t size;
    };

    using ListType = std::list<std::pair<BlockCacheKey, Entry>>;

    void evict() {
        while (used_bytes_ > capacity_bytes_ && !list_.empty()) {
            auto last = list_.end();
            --last;
            used_bytes_ -= last->second.size;
            map_.erase(last->first);
            list_.pop_back();
        }
    }

    size_t capacity_bytes_;
    size_t used_bytes_ = 0;
    mutable std::mutex mutex_;
    ListType list_;
    std::unordered_map<BlockCacheKey, typename ListType::iterator, BlockCacheKeyHash> map_;
};

}  // namespace core
}  // namespace minikv
