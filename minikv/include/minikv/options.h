#pragma once
#include <cstddef>
#include <cstdint>
#include <string>

namespace minikv {
namespace core {
enum class CompressionType : uint8_t;
}  // namespace core

struct Options {
    size_t memtable_size = 4 * 1024 * 1024;   // 4MB
    size_t block_size = 4 * 1024;              // 4KB
    size_t lru_cache_capacity = 8 * 1024 * 1024;  // 8MB
    int max_level = 7;
    size_t level0_compaction_trigger = 4;
    // T2.5 write stall: block writers when immutable or L0 too deep.
    size_t max_immutable_memtables = 4;
    size_t level0_stop_writes_trigger = 8;
    // If true, return Status::Busy instead of blocking on stall.
    bool write_stall_return_busy = false;
    bool wal_sync = true;
    bool bloom_filter_enabled = true;
    double bloom_false_positive_rate = 0.01;
    std::string db_path;

    // Compression applied to each SSTable data block before it is written.
    // The on-disk block header records the actual scheme so readers can
    // always decompress correctly regardless of this option at read time.
    //   0 = none, 1 = snappy, 2 = zstd
    uint8_t compression = 1;  // default: snappy

    // E4 test hook: first N CompactionManager merges return IOError (then real).
    // Production must leave at 0.
    int compaction_fail_inject = 0;
};

struct ReadOptions {};
struct WriteOptions {
    bool sync = true;
};

}  // namespace minikv
