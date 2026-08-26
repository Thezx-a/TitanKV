#pragma once
#include <memory>
#include <string>
#include <utility>
#include <vector>
#include "core/sstable_reader.h"
#include "minikv/iterator.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// Block-lazy iterator over one SSTable (T2.1).
// Loads one data block at a time — memory O(block), not O(file).
class SSTableIterator : public Iterator {
public:
    explicit SSTableIterator(std::shared_ptr<SSTableReader> reader);

    bool valid() const override;
    void seekToFirst() override;
    void seek(const Slice& target) override;
    void next() override;
    Slice key() const override;
    Slice value() const override;
    Status status() const override;

private:
    bool loadBlock(size_t block_index);
    void clearBlock();

    std::shared_ptr<SSTableReader> reader_;
    std::vector<std::pair<std::string, std::string>> block_entries_;
    size_t block_index_ = 0;
    size_t entry_index_ = 0;
    Status status_;
};

}  // namespace core
}  // namespace minikv
