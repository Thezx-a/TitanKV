#pragma once

#include <cstdint>
#include <mutex>
#include <string>
#include <vector>

#include "core/version.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

// Manifest — durable record of Version mutations (which SST files exist
// and at which level). On DBImpl::open, the MANIFEST is replayed so the
// in-memory Version reflects every SST produced across previous sessions.
//
// === On-disk record format ===
// Each record is appended atomically and fsync'd:
//     [crc(4)][payload_size(4)][payload...]
// payload:
//     type     : 1 byte (kAddFile=1, kRemoveFile=2, kReset=3)
//     level    : 4 bytes (LE uint32)
//     file_no  : 8 bytes (LE uint64) — Monotonic id assigned by Version
//     path_len : 4 bytes (LE uint32)
//     path     : path_len bytes (raw)
// kReset records simply mark a soft clear of the in-memory SST roster on
// replay. Prefer rewriteSnapshot() (new MANIFEST with full kAdd snapshot)
// over appending an empty kReset during recover.
//
// Recovery:
//   Read records sequentially, verify CRC, apply to in-memory levels
//   vector. A truncated/corrupt tail is dropped AND the file is ftruncated
//   to the last good offset so later appends are not stranded ([P0-3]).

class Manifest {
public:
    // level_count = max_level + 1 (indices 0..max_level inclusive).
    // Default 8 matches Options::max_level=7.
    explicit Manifest(const std::string& db_path, int level_count = 8);
    ~Manifest();

    // Open MANIFEST file (creating it if absent), replay records, and fix up
    // in-memory levels_. Returns IOError on filesystem failure.
    // If on-disk level span exceeds configured_level_count_, keeps disk size
    // and logs a warning (never silently drop SST refs).
    Status open();

    // Append a single AddFile record. Fsync is caller-controlled.
    Status recordAddFile(int level, const std::string& path, uint64_t file_no);
    Status recordRemoveFile(int level, const std::string& path, uint64_t file_no);
    Status recordReset();
    Status sync();

    // After recover() replay: archive old MANIFEST -> MANIFEST.bak, write a fresh
    // MANIFEST that contains ONLY kAdd for every currently-live SST (full snapshot).
    // Future flush/compaction edits append to this new file. Avoids empty kReset.
    Status rewriteSnapshot();

    // Snapshot of currently-tracked SSTables per level.
    const std::vector<std::vector<SSTableMeta>>& levels() const { return levels_; }
    size_t totalFiles() const;

    // For debugging / tests.
    const std::string& path() const { return manifest_path_; }
    int configuredLevelCount() const { return configured_level_count_; }

private:
    enum RecordType : uint8_t {
        kReset = 0,
        kAdd   = 1,
        kDel   = 2,
    };
    Status writeRecord(RecordType type, int level,
                       const std::string& path, uint64_t file_no);
    Status writeRecordToFd(int fd, RecordType type, int level,
                           const std::string& path, uint64_t file_no);
    Status replay();
    void recoverActivePathUnlocked();  // prefer MANIFEST, else MANIFEST.new, else bak

    std::string manifest_path_;
    int        fd_ = -1;
    int        configured_level_count_ = 8;
    std::vector<std::vector<SSTableMeta>> levels_;
    mutable std::mutex mutex_;
};

}  // namespace core
}  // namespace minikv