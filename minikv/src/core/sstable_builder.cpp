#include "core/sstable_builder.h"

#include <fcntl.h>
#include <unistd.h>

#include <cstring>

#include "core/compression.h"
#include "core/internal_key.h"
#include "utils/coding.h"
#include "utils/crc32.h"

namespace minikv {
namespace core {

SSTableBuilder::SSTableBuilder(const std::string& path,
                               size_t            block_size,
                               CompressionType  compression)
    : path_(path),
      fd_(-1),
      block_size_(block_size),
      data_block_(block_size),
      compression_(compression),
      offset_(0),
      finished_(false),
      entry_count_(0) {
    fd_ = ::open(path.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
}

SSTableBuilder::~SSTableBuilder() {
    if (!finished_) finish();
    if (fd_ >= 0) ::close(fd_);
}

Status SSTableBuilder::add(const Slice& internalKey,
                           const Slice& userKey,
                           const Slice& value) {
    data_block_.add(internalKey, value);
    bloom_keys_.emplace_back(userKey.toString());
    last_key_ = internalKey.toString();
    entry_count_++;
    if (data_block_.size() >= block_size_) {
        return flushDataBlock();
    }
    return Status::Ok();
}

Status SSTableBuilder::flushDataBlock() {
    if (data_block_.empty()) return Status::Ok();
    Slice raw = data_block_.finish();
    size_t uncompressed_size = raw.size();

    std::string payload;
    payload.assign(raw.data(), raw.size());
    CompressionType used = CompressionType::kNone;
    if (compression_ != CompressionType::kNone) {
        std::string compressed;
        Status s = compressBlock(compression_, raw, compressed);
        if (s.ok() && compressed.size() < uncompressed_size) {
            payload.swap(compressed);
            used = compression_;
        }
    }

    uint32_t crc = utils::crc32c(payload.data(), static_cast<int>(payload.size()));
    uint64_t block_offset = offset_;

    char header[kSSTableBlockHeader];
    utils::encodeFixed32(header,     crc);
    utils::encodeFixed32(header + 4, static_cast<uint32_t>(payload.size()));
    utils::encodeFixed32(header + 8, static_cast<uint32_t>(uncompressed_size));
    header[12] = static_cast<char>(static_cast<uint8_t>(used));

    ssize_t n = ::write(fd_, header, kSSTableBlockHeader);
    if (n != static_cast<ssize_t>(kSSTableBlockHeader))
        return Status::IOError("SSTable write block header failed");
    n = ::write(fd_, payload.data(), payload.size());
    if (n != static_cast<ssize_t>(payload.size()))
        return Status::IOError("SSTable write block data failed");
    offset_ += kSSTableBlockHeader + payload.size();

    std::string indexEntry;
    utils::encodeVariant32(indexEntry, static_cast<uint32_t>(last_key_.size()));
    indexEntry.append(last_key_);
    uint64_t total = kSSTableBlockHeader + payload.size();
    char handle[16];
    utils::encodeFixed64(handle,        block_offset);
    utils::encodeFixed64(handle + 8,    total);
    indexEntry.append(handle, 16);
    index_block_.append(indexEntry);

    data_block_ = BlockBuilder(block_size_);
    return Status::Ok();
}

Status SSTableBuilder::finish() {
    if (finished_) return Status::Ok();
    finished_ = true;
    flushDataBlock();

    {
        size_t n = bloom_keys_.empty() ? 1 : bloom_keys_.size();
        bloom_ = std::make_unique<BloomFilter>(n, 0.01);
        for (const auto& k : bloom_keys_) bloom_->add(Slice(k));
        bloom_->persist(path_ + ".bloom");
        bloom_keys_.clear();
        bloom_keys_.shrink_to_fit();
    }

    uint64_t index_offset = offset_;
    uint32_t index_crc = utils::crc32c(index_block_.data(),
                                         static_cast<int>(index_block_.size()));
    char idxHeader[8];
    utils::encodeFixed32(idxHeader,     index_crc);
    utils::encodeFixed32(idxHeader + 4, static_cast<uint32_t>(index_block_.size()));
    ssize_t n1 = ::write(fd_, idxHeader, 8);
    ssize_t n2 = ::write(fd_, index_block_.data(), index_block_.size());
    if (n1 != 8 || n2 != static_cast<ssize_t>(index_block_.size()))
        return Status::IOError("SSTable write index block failed");
    offset_ += 8 + index_block_.size();

    writeFooter();
    ::fdatasync(fd_);
    return Status::Ok();
}

void SSTableBuilder::writeFooter() {
    char footer[kSSTableFooterSize];
    std::memset(footer, 0, kSSTableFooterSize);
    utils::encodeFixed64(footer,        offset_ - (8 + index_block_.size()));
    utils::encodeFixed64(footer + 8,    index_block_.size() + 8);
    footer[16] = static_cast<char>(kSSTableFormatVersion);
    utils::encodeFixed64(footer + 40,   kSSTableMagic);
    ::write(fd_, footer, kSSTableFooterSize);
    offset_ += kSSTableFooterSize;
}

uint64_t SSTableBuilder::fileSize() const { return offset_; }

}  // namespace core
}  // namespace minikv