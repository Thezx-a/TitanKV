#include "core/memtable.h"

namespace minikv {
namespace core {

MemTable::MemTable(size_t max_size)
    : table_(std::make_unique<SkipList>()), max_size_(max_size) {}

void MemTable::put(const Slice& userKey, const Slice& value, uint64_t seq, bool isDelete) {
    ValueType type = isDelete ? ValueType::kDeletion : ValueType::kValue;
    std::string ikey = InternalKeyEncode(userKey, seq, type);
    std::string val = isDelete ? "" : value.toString();
    {
        std::unique_lock<std::shared_mutex> lock(mutex_);
        table_->put(ikey, val);
        entry_count_++;
    }
}

PointLookup MemTable::lookup(const Slice& userKey, uint64_t seq, std::string* value) const {
    (void)seq;  // snapshot filtering is T2.2; newest version wins today
    std::shared_lock<std::shared_mutex> lock(mutex_);
    // Seek key: max seq + kValue sorts first among this user key (InternalKeyCompare).
    std::string seek = InternalKeyEncode(userKey, kMaxSequenceNumber, ValueType::kValue);
    SkipNode* node = table_->findGreaterOrEqual(Slice(seek));
    if (!node) return PointLookup::kMiss;
    Slice uk = InternalKeyUserKey(Slice(node->key));
    if (uk.compare(userKey) != 0) return PointLookup::kMiss;
    if (IsDeletion(Slice(node->key))) return PointLookup::kTombstone;
    if (value) *value = node->value;
    return PointLookup::kValue;
}

std::optional<std::string> MemTable::get(const Slice& userKey, uint64_t seq) const {
    std::string v;
    if (lookup(userKey, seq, &v) == PointLookup::kValue) return v;
    return std::nullopt;
}

std::vector<MemTableEntry> MemTable::entries() const {
    std::shared_lock<std::shared_mutex> lock(mutex_);
    auto raw = table_->entries();
    std::vector<MemTableEntry> result;
    result.reserve(raw.size());
    for (auto& [k, v] : raw) result.push_back({std::move(k), std::move(v)});
    return result;
}

size_t MemTable::approximateMemoryUsage() const {
    std::shared_lock<std::shared_mutex> lock(mutex_);
    return table_->approximateMemoryUsage();
}

bool MemTable::shouldFlush() const {
    return approximateMemoryUsage() >= max_size_;
}

bool MemTable::empty() const {
    std::shared_lock<std::shared_mutex> lock(mutex_);
    return table_->empty();
}

}  // namespace core
}  // namespace minikv
