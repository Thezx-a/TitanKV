#include "core/block.h"
#include <algorithm>
#include <cstring>
#include "core/internal_key.h"
#include "utils/coding.h"
#include "utils/crc32.h"

namespace minikv {
namespace core {

static const int kRestartInterval = 16;

BlockBuilder::BlockBuilder(size_t block_size)
    : block_size_(block_size), finished_(false), restart_counter_(0) {
    restarts_.push_back(0);
}

void BlockBuilder::add(const Slice& key, const Slice& value) {
    size_t shared = 0;
    if (restart_counter_ < kRestartInterval && !last_key_.empty()) {
        size_t minLen = std::min(last_key_.size(), key.size());
        while (shared < minLen && last_key_[shared] == key[shared]) ++shared;
    } else if (restart_counter_ >= kRestartInterval) {
        restarts_.push_back(static_cast<uint32_t>(buffer_.size()));
        restart_counter_ = 0;
        shared = 0;
    }
    size_t non_shared = key.size() - shared;
    std::string header;
    utils::encodeVariant32(header, static_cast<uint32_t>(shared));
    utils::encodeVariant32(header, static_cast<uint32_t>(non_shared));
    utils::encodeVariant32(header, static_cast<uint32_t>(value.size()));
    buffer_.append(header);
    buffer_.append(key.data() + shared, non_shared);
    buffer_.append(value.data(), value.size());
    last_key_ = key.toString();
    ++restart_counter_;
}

Slice BlockBuilder::finish() {
    // LevelDB-compatible trailer: restart offsets[], then restart count.
    for (uint32_t off : restarts_) {
        char buf[4];
        utils::encodeFixed32(buf, off);
        buffer_.append(buf, 4);
    }
    char cnt[4];
    utils::encodeFixed32(cnt, static_cast<uint32_t>(restarts_.size()));
    buffer_.append(cnt, 4);
    finished_ = true;
    return Slice(buffer_);
}

BlockReader::BlockReader(const Slice& block_data)
    : data_(block_data), num_entries_(0), restarts_offset_(0) {
    if (block_data.size() < 4) return;
    uint32_t restarts_count = utils::decodeFixed32(
        block_data.data() + block_data.size() - 4);
    restarts_offset_ = static_cast<uint32_t>(block_data.size() - 4 - restarts_count * 4);
    num_entries_ = restarts_count;
}

std::optional<std::string> BlockReader::get(const Slice& key) const {
    size_t offset = 0;
    std::string lastKey;
    while (offset < restarts_offset_) {
        uint32_t shared, nonShared, valLen;
        const char* p = data_.data() + offset;
        const char* limit = data_.data() + restarts_offset_;
        uint32_t consumed = 0;
        if (!utils::decodeVariant32(p, limit, shared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, nonShared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, valLen, consumed)) break;
        p += consumed;
        std::string currentKey = lastKey.substr(0, shared);
        currentKey.append(p, nonShared);
        p += nonShared;
        if (currentKey == key.toString()) {
            return std::string(p, valLen);
        }
        if (currentKey > key.toString()) return std::nullopt;
        lastKey = std::move(currentKey);
        offset = (p - data_.data()) + valLen;
    }
    return std::nullopt;
}

PointLookup BlockReader::lookupByUserKey(const Slice& userKey, std::string* value) const {
    size_t offset = 0;
    std::string lastKey;
    while (offset < restarts_offset_) {
        uint32_t shared, nonShared, valLen;
        const char* p = data_.data() + offset;
        const char* limit = data_.data() + restarts_offset_;
        uint32_t consumed = 0;
        if (!utils::decodeVariant32(p, limit, shared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, nonShared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, valLen, consumed)) break;
        p += consumed;
        std::string currentKey = lastKey.substr(0, shared);
        currentKey.append(p, nonShared);
        p += nonShared;

        Slice ik(currentKey);
        Slice uk = InternalKeyUserKey(ik);
        int cmp = uk.compare(userKey);
        if (cmp == 0) {
            if (IsDeletion(ik)) return PointLookup::kTombstone;
            if (value) value->assign(p, valLen);
            return PointLookup::kValue;
        }
        if (cmp > 0) break;

        lastKey = std::move(currentKey);
        offset = static_cast<size_t>(p + valLen - data_.data());
    }
    return PointLookup::kMiss;
}

std::optional<std::string> BlockReader::getByUserKey(const Slice& userKey) const {
    std::string v;
    if (lookupByUserKey(userKey, &v) == PointLookup::kValue) return v;
    return std::nullopt;
}

void BlockReader::forEach(
    const std::function<void(const Slice& key, const Slice& value)>& cb) const {
    size_t offset = 0;
    std::string lastKey;
    while (offset < restarts_offset_) {
        uint32_t shared, nonShared, valLen;
        const char* p = data_.data() + offset;
        const char* limit = data_.data() + restarts_offset_;
        uint32_t consumed = 0;
        if (!utils::decodeVariant32(p, limit, shared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, nonShared, consumed)) break;
        p += consumed;
        if (!utils::decodeVariant32(p, limit, valLen, consumed)) break;
        p += consumed;
        std::string currentKey = lastKey.substr(0, shared);
        currentKey.append(p, nonShared);
        p += nonShared;
        Slice keySlice(currentKey);
        Slice valSlice(p, valLen);
        cb(keySlice, valSlice);
        lastKey = std::move(currentKey);
        offset = static_cast<size_t>((p - data_.data()) + valLen);
    }
}

}  // namespace core
}  // namespace minikv