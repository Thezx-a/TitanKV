#include "core/memtable_iterator.h"

#include <algorithm>

#include "core/internal_key.h"

namespace minikv {
namespace core {

MemTableIterator::MemTableIterator(std::shared_ptr<const MemTable> table)
    : table_(std::move(table)), use_snapshot_(false), status_(Status::Ok()) {
    if (table_ && table_->table()) {
        it_ = std::make_unique<SkipList::Iterator>(table_->table());
    } else {
        status_ = Status::Corruption("null memtable");
    }
}

MemTableIterator::MemTableIterator(std::vector<MemTableEntry> entries)
    : entries_(std::move(entries)), use_snapshot_(true), status_(Status::Ok()) {
    std::sort(entries_.begin(), entries_.end(),
              [](const MemTableEntry& a, const MemTableEntry& b) {
                  return InternalKeyCompare(Slice(a.internal_key), Slice(b.internal_key)) < 0;
              });
}

bool MemTableIterator::valid() const {
    if (!status_.ok()) return false;
    if (use_snapshot_) return index_ < entries_.size();
    return it_ && it_->valid();
}

void MemTableIterator::seekToFirst() {
    if (use_snapshot_) {
        index_ = 0;
        return;
    }
    if (it_) it_->seekToFirst();
}

void MemTableIterator::seek(const Slice& target) {
    if (use_snapshot_) {
        index_ = 0;
        while (index_ < entries_.size() &&
               InternalKeyCompare(Slice(entries_[index_].internal_key), target) < 0) {
            ++index_;
        }
        return;
    }
    if (it_) it_->seek(target);
}

void MemTableIterator::next() {
    if (!valid()) return;
    if (use_snapshot_) {
        ++index_;
        return;
    }
    if (it_) it_->next();
}

Slice MemTableIterator::key() const {
    if (!valid()) return Slice();
    if (use_snapshot_) return Slice(entries_[index_].internal_key);
    return it_->key();
}

Slice MemTableIterator::value() const {
    if (!valid()) return Slice();
    if (use_snapshot_) return Slice(entries_[index_].value);
    return it_->value();
}

Status MemTableIterator::status() const { return status_; }

}  // namespace core
}  // namespace minikv
