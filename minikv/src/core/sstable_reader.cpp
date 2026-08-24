#include "core/sstable_reader.h"
#include "core/block_cache.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>

#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/compression.h"
#include "utils/coding.h"
#include "utils/crc32.h"

namespace minikv {
namespace core {

std::unique_ptr<SSTableReader> SSTableReader::open(const std::string& path,
                                                   BlockCache* cache) {
    auto reader = std::unique_ptr<SSTableReader>(new SSTableReader());
    reader->path_ = path;
    reader->block_cache_ = cache;
    reader->fd_   = ::open(path.c_str(), O_RDONLY);
    if (reader->fd_ < 0) return nullptr;

    struct stat st;
    ::fstat(reader->fd_, &st);
    reader->file_size_ = st.st_size;
    if (static_cast<size_t>(st.st_size) < kSSTableFooterSize) return nullptr;

    char footer[kSSTableFooterSize];
    ::lseek(reader->fd_, st.st_size - static_cast<off_t>(kSSTableFooterSize), SEEK_SET);
    if (::read(reader->fd_, footer, kSSTableFooterSize) != static_cast<ssize_t>(kSSTableFooterSize))
        return nullptr;

    uint64_t magic = utils::decodeFixed64(footer + 40);
    if (magic != kSSTableMagic) return nullptr;

    reader->format_version_ = static_cast<uint8_t>(footer[16]);
    if (reader->format_version_ > kSSTableFormatVersion) return nullptr;

    reader->index_offset_ = utils::decodeFixed64(footer);
    reader->index_size_   = utils::decodeFixed64(footer + 8);

    ::lseek(reader->fd_, reader->index_offset_, SEEK_SET);
    char idxHeader[8];
    if (::read(reader->fd_, idxHeader, 8) != 8) return nullptr;
    uint32_t idxLen = utils::decodeFixed32(idxHeader + 4);
    reader->index_data_.resize(idxLen);
    if (::read(reader->fd_, reader->index_data_.data(), idxLen) != static_cast<ssize_t>(idxLen))
        return nullptr;

    size_t offset = 0;
    while (offset < reader->index_data_.size()) {
        const char* p     = reader->index_data_.data() + offset;
        const char* limit = reader->index_data_.data() + reader->index_data_.size();
        uint32_t    keyLen, consumed;
        if (!utils::decodeVariant32(p, limit, keyLen, consumed)) break;
        p += consumed;
        std::string lastKey(p, keyLen);
        p += keyLen;
        IndexEntry e;
        e.last_key = std::move(lastKey);
        e.handle.offset = utils::decodeFixed64(p);
        e.handle.size   = utils::decodeFixed64(p + 8);
        p += 16;
        reader->index_entries_.push_back(std::move(e));
        offset = static_cast<size_t>(p - reader->index_data_.data());
    }

    reader->bloom_ = BloomFilter::load(path + ".bloom");
    return reader;
}

Status SSTableReader::readBlock(const BlockHandle& h, std::string* out) const {
    if (h.size < kSSTableBlockHeader)
        return Status::Corruption("SSTable block handle size too small");

    if (block_cache_) {
        BlockCacheKey key{path_, h.offset};
        if (auto cached = block_cache_->get(key)) {
            *out = *cached;
            return Status::Ok();
        }
    }

    ::lseek(fd_, static_cast<off_t>(h.offset), SEEK_SET);
    char hdr[kSSTableBlockHeader];
    if (::read(fd_, hdr, kSSTableBlockHeader) != static_cast<ssize_t>(kSSTableBlockHeader))
        return Status::IOError("failed to read block header");

    uint32_t           crc              = utils::decodeFixed32(hdr);
    uint32_t           payload_size     = utils::decodeFixed32(hdr + 4);
    uint32_t           uncompressed_sz  = utils::decodeFixed32(hdr + 8);
    CompressionType    type             = static_cast<CompressionType>(
                                            static_cast<uint8_t>(hdr[12]));

    if (payload_size == 0) {
        out->clear();
        return Status::Ok();
    }

    std::string payload(payload_size, '\0');
    if (::read(fd_, payload.data(), payload_size) != static_cast<ssize_t>(payload_size))
        return Status::IOError("failed to read block payload");

    uint32_t actual = utils::crc32c(payload.data(), static_cast<int>(payload_size));
    if (actual != crc)
        return Status::Corruption("SSTable block CRC mismatch");

    Status ds = decompressBlock(type, Slice(payload), uncompressed_sz, *out);
    if (ds.ok() && block_cache_) {
        BlockCacheKey key{path_, h.offset};
        block_cache_->put(key, *out);
    }
    return ds;
}

std::optional<std::string> SSTableReader::get(const Slice& userKey) const {
    if (index_entries_.empty()) return std::nullopt;
    if (bloom_ && !bloom_->mightContain(userKey)) return std::nullopt;

    // First index entry whose last user key is >= search key.
    auto it = std::lower_bound(
        index_entries_.begin(), index_entries_.end(), userKey.toString(),
        [](const IndexEntry& e, const std::string& k) {
            Slice lastUK = InternalKeyUserKey(Slice(e.last_key));
            return lastUK.compare(Slice(k)) < 0;
        });
    if (it == index_entries_.end()) return std::nullopt;

    std::string block;
    Status s = readBlock(it->handle, &block);
    if (!s.ok()) return std::nullopt;

    BlockReader reader{Slice(block)};
    return reader.getByUserKey(userKey);
}

Status SSTableReader::scan(const Slice& start, const Slice& end,
                           std::function<void(const Slice&, const Slice&)> cb) const {
    for (const auto& e : index_entries_) {
        Slice lastUK = InternalKeyUserKey(Slice(e.last_key));
        if (!end.empty() && lastUK.compare(end) > 0) break;

        std::string block;
        Status s = readBlock(e.handle, &block);
        if (!s.ok()) return s;

        BlockReader reader{Slice(block)};
        reader.forEach([&](const Slice& k, const Slice& v) {
            Slice uk = InternalKeyUserKey(k);
            if (!start.empty() && uk.compare(start) < 0) return;
            if (!end.empty() && uk.compare(end) > 0) return;
            cb(k, v);
        });
    }
    return Status::Ok();
}

}  // namespace core
}  // namespace minikv