#pragma once
#include <memory>
#include <queue>
#include <string>
#include <vector>
#include "core/internal_key.h"
#include "minikv/iterator.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// Min-heap merge of child iterators (LevelDB / RocksDB MergingIterator pattern).
// Ordering uses InternalKeyCompare (not raw string bytes).
// When keys compare equal, the earlier child wins (caller should order:
// mem, imm, newest L0 … oldest).
class MergingIterator : public Iterator {
public:
    explicit MergingIterator(std::vector<std::unique_ptr<Iterator>> children);

    bool valid() const override;
    void seekToFirst() override;
    void seek(const Slice& target) override;
    void next() override;
    Slice key() const override;
    Slice value() const override;
    Status status() const override;

private:
    struct HeapItem {
        size_t child_index;
        std::string key;
        // Min-heap: smallest InternalKeyCompare first.
        bool operator>(const HeapItem& o) const {
            int c = InternalKeyCompare(Slice(key), Slice(o.key));
            if (c != 0) return c > 0;
            // Tie-break: prefer earlier child (smaller index).
            return child_index > o.child_index;
        }
    };

    void rebuildHeap();
    void pushChild(size_t i);

    std::vector<std::unique_ptr<Iterator>> children_;
    std::priority_queue<HeapItem, std::vector<HeapItem>, std::greater<HeapItem>> heap_;
    size_t current_ = static_cast<size_t>(-1);
    Status status_;
};

}  // namespace core
}  // namespace minikv
