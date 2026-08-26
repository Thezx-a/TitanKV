#pragma once
#include <cstdint>
#include <memory>
#include <string>
#include "core/memtable.h"
#include "core/skip_list.h"
#include "minikv/iterator.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// Lazy MemTable iterator (M9): walks SkipList in place; keeps MemTable alive.
// Does not snapshot-copy all entries. Concurrent writes on a live MemTable are
// not snapshot-isolated (immutable MemTables used after flush are write-frozen).
class MemTableIterator : public Iterator {
public:
    explicit MemTableIterator(std::shared_ptr<const MemTable> table);
    // Legacy overload: materialize then walk (kept for any leftover call sites).
    explicit MemTableIterator(std::vector<MemTableEntry> entries);

    bool valid() const override;
    void seekToFirst() override;
    void seek(const Slice& target) override;
    void next() override;
    Slice key() const override;
    Slice value() const override;
    Status status() const override;

private:
    std::shared_ptr<const MemTable> table_;
    std::unique_ptr<SkipList::Iterator> it_;
    // Fallback path when constructed from a vector snapshot.
    std::vector<MemTableEntry> entries_;
    size_t index_ = 0;
    bool use_snapshot_ = false;
    Status status_;
};

}  // namespace core
}  // namespace minikv
