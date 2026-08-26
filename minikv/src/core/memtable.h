#pragma once
#include <atomic>
#include <memory>
#include <optional>
#include <shared_mutex>
#include <string>
#include <vector>
#include "core/skip_list.h"
#include "core/internal_key.h"
#include "minikv/slice.h"

namespace minikv {
namespace core {

struct MemTableEntry {
    std::string internal_key;   // InternalKeyEncode(user_key, seq, type)
    std::string value;
};

class MemTableIterator;

class MemTable {
public:
    explicit MemTable(size_t max_size = 4 * 1024 * 1024);

    void put(const Slice& userKey, const Slice& value, uint64_t seq, bool isDelete);
    // Three-state point lookup: miss / value / tombstone. O(log n) via skiplist.
    PointLookup lookup(const Slice& userKey, uint64_t seq, std::string* value) const;
    // Convenience: value or nullopt (tombstone and miss both nullopt).
    std::optional<std::string> get(const Slice& userKey, uint64_t seq) const;
    // Full copy for flush / SST build only — not the Get hot path (M9).
    std::vector<MemTableEntry> entries() const;
    size_t approximateMemoryUsage() const;
    bool shouldFlush() const;
    bool empty() const;

    // Exposed for MemTableIterator lazy traversal (M9).
    const SkipList* table() const { return table_.get(); }

private:
    friend class MemTableIterator;

    std::unique_ptr<SkipList> table_;
    size_t max_size_;
    std::atomic<uint64_t> entry_count_{0};
    mutable std::shared_mutex mutex_;
};

}  // namespace core
}  // namespace minikv
