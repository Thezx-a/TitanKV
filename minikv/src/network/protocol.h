#pragma once
#include <cstdint>
#include <string>
#include "minikv/slice.h"

namespace minikv {
namespace network {

static const uint16_t kProtocolMagic = 0x4D4B;  // "MK"

enum class Cmd : uint8_t {
    kPut = 1, kGet = 2, kDel = 3, kScan = 4, kBatch = 5, kDeleteRange = 6,
};

enum class ResponseStatus : uint8_t {
    kOk = 0, kNotFound = 1, kError = 2,
};

#pragma pack(push, 1)
struct RequestHeader {
    uint16_t magic;
    uint8_t cmd;
    uint32_t key_len;
    uint32_t val_len;
};

struct ResponseHeader {
    uint16_t magic;
    uint8_t status;
    uint32_t val_len;
};
#pragma pack(pop)

inline std::string encodeRequest(Cmd cmd, const Slice& key, const Slice& value) {
    std::string result;
    RequestHeader hdr;
    hdr.magic = kProtocolMagic;
    hdr.cmd = static_cast<uint8_t>(cmd);
    hdr.key_len = static_cast<uint32_t>(key.size());
    hdr.val_len = static_cast<uint32_t>(value.size());
    result.append(reinterpret_cast<const char*>(&hdr), sizeof(hdr));
    result.append(key.data(), key.size());
    result.append(value.data(), value.size());
    return result;
}

inline std::string encodeResponse(ResponseStatus status, const Slice& value) {
    std::string result;
    ResponseHeader hdr;
    hdr.magic = kProtocolMagic;
    hdr.status = static_cast<uint8_t>(status);
    hdr.val_len = static_cast<uint32_t>(value.size());
    result.append(reinterpret_cast<const char*>(&hdr), sizeof(hdr));
    result.append(value.data(), value.size());
    return result;
}

}  // namespace network
}  // namespace minikv
