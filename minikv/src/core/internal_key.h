#pragma once

#include <cstdint>
#include <string>

#include "minikv/slice.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// ValueType for internal_key trailer. Matches existing MemTable sentinel:
//   kDeletion (2) — tombstone; older versions of the same key are invisible.
//   kValue    (1) — regular put.
// Reflected in WAL record encodings and SSTable trailers.
enum class ValueType : uint8_t {
    kNone    = 0,
    kValue    = 1,
    kDeletion = 2,
};

// InternalKey — encoded key layout used by MemTable / SSTables / Iterators
// once Phase 1 WP 1.2.2 wiring is complete. Today (Phase A) only the codec
// exists; existing hash-based internal keys are still in use and will be
// migrated incrementally.
//
// Layout:
//   internal_key = user_key_bytes || trailer_bytes(8)
//   trailer      = uint64 little-endian encoding of
//                   ((seq << kTypeBits) | static_cast<uint8_t>(type))
//
// Trailer bit allocation:
//   bits 0..7   : ValueType
//   bits 8..63  : seq (56 bits, ~7.2e16 — enough for decades of writes)
//
// Sort order (used by SSTable / MemTable iterators):
//   1. user_key ascending (byte-wise); then
//   2. trailer descending — implemented in Compare() by decoding the trailer
//      as uint64 LE and reversing the seq result so larger seq sorts first.
//      Within the same seq, smaller ValueType wins (kValue=1 before kDeletion=2).
//
// All helper functions are header-only-friendly; the .cpp provides non-trivial
// pieces (encode / decode / compare).

static constexpr int   kTypeBits     = 8;
static constexpr size_t kTrailerBytes = 8;
// Max sequence stored in the 56-bit field of the trailer (M9 seek key).
static constexpr uint64_t kMaxSequenceNumber = (uint64_t{1} << 56) - 1u;

// Build an internal_key string from its parts.
std::string InternalKeyEncode(const Slice& user_key,
                              uint64_t     seq,
                              ValueType    type);

// Extract the user-key portion of an internal_key (without trailing 8 bytes).
Slice       InternalKeyUserKey(const Slice& internal_key);

// Extract the sequence number from an internal_key (last 8 bytes LE).
uint64_t   InternalKeySequence(const Slice& internal_key);

// Extract the ValueType from an internal_key.
ValueType  InternalKeyType(const Slice& internal_key);

// Total encoded length (same as input.size()).
inline size_t InternalKeyLen(const Slice& ikey) { return ikey.size(); }

// Three-way comparator implementing the sort order described above.
// Returns negative / zero / positive like strcmp.
int InternalKeyCompare(const Slice& a, const Slice& b);

// True when an internal_key is a tombstone for `user_key` at `seq`.
inline bool IsDeletion(const Slice& internal_key) {
    return InternalKeyType(internal_key) == ValueType::kDeletion;
}

// Point-lookup outcome. Tombstone must NOT be conflated with miss — otherwise
// DBImpl::get keeps scanning older levels/files and resurrected deleted keys.
enum class PointLookup : uint8_t {
    kMiss = 0,
    kValue = 1,
    kTombstone = 2,
};

}  // namespace core
}  // namespace minikv