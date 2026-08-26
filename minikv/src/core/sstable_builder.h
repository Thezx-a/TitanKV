#pragma once
#include <cstdint>
#include <memory>
#include <string>
#include <vector>
#include "core/block.h"
#include "core/bloom_filter.h"
#include "core/compression.h"
#include "minikv/slice.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// SSTable on-disk format (post Phase 1 WP 1.2.1):
//
//   +-----------------------------+
//   | data block #0               |     each block: [crc(4)]
//   |   ...                       |                [physical_size(4)]
//   | data block #N               |                [uncompressed_size(4)]
//   | index block                 |                [type(1)  = CompressionType]
//   | footer (48 bytes)           |                [payload(physical_size)]
//   +-----------------------------+
//
// The index block keeps the legacy [crc(4)][size(4)][entries...] header so it
// can be parsed without decompression.
//
// Footer layout (48 bytes total):
//   8 bytes : index_offset
//   8 bytes : index_size  (including 8-byte header)
//   1 byte  : format_version (write = 1, transparent to reader)
//   1 byte  : reserved
//  30 bytes : reserved padding (zeros)
//   8 bytes : magic  (0x4D4B53535441424C)
//
// Legacy files (version 0) used 8-byte block headers; readers detect them
// via an explicit version byte == 0 in the (newly written) footer slot.

static const uint8_t  kSSTableFormatVersion = 1;
static const uint64_t kSSTableMagic         = 0x4D4B53535441424CULL;
static const size_t   kSSTableFooterSize    = 48;
static const size_t   kSSTableBlockHeader   = 13;  // crc+psize+usize+type

class SSTableBuilder {
public:
    SSTableBuilder(const std::string& path,
                   size_t            block_size               = 4096,
                   CompressionType   compression               = CompressionType::kSnappy);
    ~SSTableBuilder();
    Status add(const Slice& internalKey, const Slice& userKey, const Slice& value);
    Status finish();
    uint64_t fileSize() const;

private:
    Status flushDataBlock();
    void   writeFooter();

    std::string          path_;
    int                  fd_;
    size_t               block_size_;
    BlockBuilder         data_block_;
    CompressionType      compression_;
    std::unique_ptr<BloomFilter> bloom_;
    std::vector<std::string> bloom_keys_;
    std::string          index_block_;
    std::string          last_key_;
    uint64_t             offset_      = 0;
    bool                 finished_    = false;
    uint64_t             entry_count_ = 0;
};

}  // namespace core
}  // namespace minikv