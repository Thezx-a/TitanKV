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

namespace {
constexpr uint32_t kMaxBlockPayload      = 64u << 20;  // 64 MiB
constexpr uint32_t kMaxBlockUncompressed = 64u << 20;
}  // namespace

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

    if (reader->index_offset_ > reader->file_size_ ||
        reader->index_size_ > reader->file_size_ - reader->index_offset_)
        return nullptr;

    ::lseek(reader->fd_, reader->index_offset_, SEEK_SET);
    char idxHeader[8];
    if (::read(reader->fd_, idxHeader, 8) != 8) return nullptr;
    uint32_t idxCrc = utils::decodeFixed32(idxHeader);
    uint32_t idxLen = utils::decodeFixed32(idxHeader + 4);
    if (idxLen > kMaxBlockPayload) return nullptr;
    if (static_cast<uint64_t>(8) + idxLen > reader->index_size_) return nullptr;
    reader->index_data_.resize(idxLen);
    if (::read(reader->fd_, reader->index_data_.data(), idxLen) != static_cast<ssize_t>(idxLen))
        return nullptr;
    uint32_t actualIdxCrc = utils::crc32c(reader->index_data_.data(),
                                          static_cast<int>(idxLen));
    if (actualIdxCrc != idxCrc) return nullptr;

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


Status SSTableReader::readDataBlock(size_t i, std::string* out) const {
    if (i >= index_entries_.size())
        return Status::InvalidArgument("data block index out of range");
    return readBlock(index_entries_[i].handle, out);
}

SSTableReader::~SSTableReader() {
    if (fd_ >= 0) {
        ::close(fd_);
        fd_ = -1;
    }
}

Status SSTableReader::readBlock(const BlockHandle& h, std::string* out) const {
    if (h.size < kSSTableBlockHeader)
        return Status::Corruption("SSTable block handle size too small");
    if (h.offset > file_size_ || h.size > file_size_ - h.offset)
        return Status::Corruption("SSTable block handle out of file bounds");

    if (block_cache_) {
        BlockCacheKey key{path_, h.offset};
        if (auto cached = block_cache_->get(key)) {
            *out = *cached;
            return Status::Ok();
        }
    }

    // pread: TableCache shares one reader across threads; lseek+read races (T2.3).
    char hdr[kSSTableBlockHeader];
    if (::pread(fd_, hdr, kSSTableBlockHeader, static_cast<off_t>(h.offset)) !=
        static_cast<ssize_t>(kSSTableBlockHeader))
        return Status::IOError("failed to read block header");

    uint32_t           crc              = utils::decodeFixed32(hdr);
    uint32_t           payload_size     = utils::decodeFixed32(hdr + 4);
    uint32_t           uncompressed_sz  = utils::decodeFixed32(hdr + 8);
    CompressionType    type             = static_cast<CompressionType>(
                                            static_cast<uint8_t>(hdr[12]));

    if (payload_size > kMaxBlockPayload || uncompressed_sz > kMaxBlockUncompressed)
        return Status::Corruption("SSTable block size exceeds limit");
    if (static_cast<uint64_t>(kSSTableBlockHeader) + payload_size > h.size)
        return Status::Corruption("SSTable block payload exceeds handle size");

    if (payload_size == 0) {
        out->clear();
        return Status::Ok();
    }

    std::string payload;
    payload.resize(payload_size);
    off_t payload_off = static_cast<off_t>(h.offset + kSSTableBlockHeader);
    if (::pread(fd_, payload.data(), payload_size, payload_off) !=
        static_cast<ssize_t>(payload_size))
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

Status SSTableReader::lookup(const Slice& userKey, std::string* value,
                             PointLookup* out) const {
    *out = PointLookup::kMiss;
    if (index_entries_.empty()) return Status::Ok();
    if (bloom_ && !bloom_->mightContain(userKey)) return Status::Ok();

    auto it = std::lower_bound(
        index_entries_.begin(), index_entries_.end(), userKey.toString(),
        [](const IndexEntry& e, const std::string& k) {
            Slice lastUK = InternalKeyUserKey(Slice(e.last_key));
            return lastUK.compare(Slice(k)) < 0;
        });
    if (it == index_entries_.end()) return Status::Ok();

    std::string block;
    Status s = readBlock(it->handle, &block);
    if (!s.ok()) return s;

    BlockReader reader{Slice(block)};
    *out = reader.lookupByUserKey(userKey, value);
    return Status::Ok();
}

std::optional<std::string> SSTableReader::get(const Slice& userKey) const {
    std::string v;
    PointLookup pl = PointLookup::kMiss;
    Status s = lookup(userKey, &v, &pl);
    if (!s.ok() || pl != PointLookup::kValue) return std::nullopt;
    return v;
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
