#pragma once
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <vector>
#include "core/bloom_filter.h"
#include "core/block.h"
#include "minikv/slice.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

class BlockCache;

class SSTableReader {
public:
    static std::unique_ptr<SSTableReader> open(const std::string& path,
                                               BlockCache* cache = nullptr);
    std::optional<std::string> get(const Slice& userKey) const;
    bool mightContain(const Slice& key) const { return bloom_ && bloom_->mightContain(key); }
    Status scan(const Slice& start, const Slice& end,
                std::function<void(const Slice&, const Slice&)> callback) const;

    const std::string& path() const { return path_; }
    uint64_t fileSize() const { return file_size_; }
    uint8_t  formatVersion() const { return format_version_; }

private:
    struct IndexEntry {
        std::string  last_key;      // full internal_key of last entry in block
        BlockHandle  handle;        // offset, total size (incl. 13B header)
    };

    Status readBlock(const BlockHandle& h, std::string* out) const;

    std::string                       path_;
    int                               fd_            = -1;
    uint64_t                          file_size_     = 0;
    uint8_t                           format_version_ = 0;
    uint64_t                          index_offset_  = 0;
    uint64_t                          index_size_    = 0;
    std::string                       index_data_;
    std::vector<IndexEntry>           index_entries_;
    std::unique_ptr<BloomFilter>      bloom_;
    BlockCache*                       block_cache_ = nullptr;
};

}  // namespace core
}  // namespace minikv