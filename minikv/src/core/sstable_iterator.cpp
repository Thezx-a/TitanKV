#include "core/sstable_iterator.h"

#include "core/block.h"
#include "core/internal_key.h"

namespace minikv {
namespace core {

SSTableIterator::SSTableIterator(std::shared_ptr<SSTableReader> reader)
    : reader_(std::move(reader)), status_(Status::Ok()) {
    if (!reader_) {
        status_ = Status::IOError("null sstable reader");
        return;
    }
    seekToFirst();
}

void SSTableIterator::clearBlock() {
    block_entries_.clear();
    entry_index_ = 0;
}

bool SSTableIterator::loadBlock(size_t block_index) {
    clearBlock();
    if (!reader_ || !status_.ok()) return false;
    if (block_index >= reader_->numDataBlocks()) return false;

    std::string raw;
    Status s = reader_->readDataBlock(block_index, &raw);
    if (!s.ok()) {
        status_ = s;
        return false;
    }
    BlockReader br{Slice(raw)};
    br.forEach([this](const Slice& k, const Slice& v) {
        block_entries_.emplace_back(k.toString(), v.toString());
    });
    block_index_ = block_index;
    entry_index_ = 0;
    return !block_entries_.empty();
}

bool SSTableIterator::valid() const {
    return status_.ok() && entry_index_ < block_entries_.size();
}

void SSTableIterator::seekToFirst() {
    if (!reader_ || !status_.ok()) return;
    if (reader_->numDataBlocks() == 0) {
        clearBlock();
        return;
    }
    loadBlock(0);
}

void SSTableIterator::seek(const Slice& target) {
    if (!reader_ || !status_.ok()) return;
    const size_t n = reader_->numDataBlocks();
    if (n == 0) {
        clearBlock();
        return;
    }
    // Linear scan of blocks (index last_key not exposed as seek helper yet).
    for (size_t bi = 0; bi < n; ++bi) {
        if (!loadBlock(bi)) return;
        while (entry_index_ < block_entries_.size() &&
               InternalKeyCompare(Slice(block_entries_[entry_index_].first), target) < 0) {
            ++entry_index_;
        }
        if (entry_index_ < block_entries_.size()) return;
    }
    clearBlock();
}

void SSTableIterator::next() {
    if (!valid()) return;
    ++entry_index_;
    if (entry_index_ < block_entries_.size()) return;
    // Advance to next non-empty block.
    size_t next_bi = block_index_ + 1;
    while (reader_ && next_bi < reader_->numDataBlocks()) {
        if (loadBlock(next_bi)) return;
        if (!status_.ok()) return;
        ++next_bi;
    }
    clearBlock();
}

Slice SSTableIterator::key() const {
    if (!valid()) return Slice();
    return Slice(block_entries_[entry_index_].first);
}

Slice SSTableIterator::value() const {
    if (!valid()) return Slice();
    return Slice(block_entries_[entry_index_].second);
}

Status SSTableIterator::status() const { return status_; }

}  // namespace core
}  // namespace minikv
