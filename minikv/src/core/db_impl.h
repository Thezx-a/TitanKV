#pragma once
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <deque>
#include <memory>
#include <mutex>
#include <string>
#include <vector>
#include "core/compaction.h"
#include "core/block_cache.h"
#include "core/table_cache.h"
#include "core/manifest.h"
#include "core/memtable.h"
#include "core/memtable_iterator.h"
#include "core/version.h"
#include "core/wal.h"
#include "minikv/db.h"
#include "minikv/options.h"
#include "minikv/write_batch.h"

namespace minikv {
namespace core {

class DBImpl : public ::minikv::DB {
public:
    explicit DBImpl(const Options& options);
    ~DBImpl();

    static Status open(const Options& options, std::unique_ptr<DB>* dbptr);

    Status put(const WriteOptions& opts, const Slice& key, const Slice& value) override;
    Status get(const ReadOptions& opts, const Slice& key, std::string* value) override;
    Status del(const WriteOptions& opts, const Slice& key) override;
    Status write(const WriteOptions& opts, const WriteBatch& batch) override;
    std::unique_ptr<Iterator> newIterator(const ReadOptions& opts) override;
    void compact() override;
    void waitFlush() override;
    void waitCompaction() override;

private:
    void maybeFlush();
    // T2.5: block (or Busy) until immutable/L0 under stall limits.
    Status waitUntilWritable();
    Status recover();
    // Garbage-collect orphan SSTables on disk (crash-left files not in MANIFEST).
    Status purgeOrphanSSTables();

    // One MemTable generation <-> one WAL file (wal-<N>.log). Flush deletes old files.
    std::string makeWalPath(uint64_t file_number) const;
    std::vector<std::pair<uint64_t, std::string>> listWalFiles() const;
    Status openWal(uint64_t file_number);
    Status rotateWal();
    Status replayWalFile(const std::string& path);

    // ---------------------------------------------------------------------
    // Group Commit (Batched WAL + amortized fsync) — see contract in .cpp
    // ---------------------------------------------------------------------
    struct GroupEntry {
        WriteBatch    batch;
        WriteOptions  opts;
        Status        status;
        bool          done = false;
    };

    Status doWriteGroup(const std::vector<std::shared_ptr<GroupEntry>>& group);

    static constexpr size_t      kMaxGroupSize        = 32;
    static constexpr std::chrono::microseconds kGroupCommitTimeout{500};

    // ---------------------------------------------------------------------
    // Background flush (decouple heavy SSTable build from write path).
    // ---------------------------------------------------------------------
    // A dedicated flush_thread_ drains flush_queue_ in FIFO order. The write
    // path under write_mutex_ only does the cheap "swap memtable → immutable,
    // rotate WAL, push to flush_queue_" then notifies flush_cv_ — it never
    // waits for SSTable build. The flush thread performs the heavy IO
    // (SkipList scan → SSTableBuilder → addLevelFile → unlink WAL) outside
    // any write lock; it only re-acquires write_mutex_ briefly to erase the
    // finished MemTable from immutable_list_.
    //
    // Visibility contract:
    //   - immutable_list_ (under write_mutex_) is the "read-visible" set of
    //     MemTables whose data is not yet durable in any SST. Get/newIterator
    //     snapshot the whole vector under the lock, then iterate lock-free.
    //   - flush_queue_ (under flush_mu_) is the "to-do" set for the flush
    //     thread. Each entry references the same shared_ptr<MemTable> that's
    //     also in immutable_list_ — so the flush thread can work on the data
    //     without holding write_mutex_.
    //   - Order: memtable_ is newest, immutable_list_.back() next, ..., front()
    //     oldest, then L0 SSTables (time-reverse), then L1+ SSTables.
    // ---------------------------------------------------------------------
    struct FlushEntry {
        std::shared_ptr<MemTable>   memtable;
        std::vector<std::string>    wals;
    };

    void flushLoop();
    // Heavy work: build one SSTable from `flushing`, register it in Version,
    // then unlink the WAL files that belonged to this generation. Called only
    // by the flush thread. No lock is held during IO; write_mutex_ is taken
    // only briefly at the end to erase the finished entry from immutable_list_.
    Status flushOne(const std::shared_ptr<MemTable>& flushing,
                    const std::vector<std::string>& wals);

    Options options_;
    std::string db_path_;
    std::unique_ptr<WAL>      wal_;
    std::string               current_wal_path_;
    // WALs whose data is fully covered by the memtable about to flush / just flushed.
    std::vector<std::string>  obsolete_wal_paths_;
    std::unique_ptr<Manifest> manifest_;
    std::shared_ptr<MemTable> memtable_;
    // MemTables that have been frozen (rotated out of memtable_) but whose
    // data has not yet been written into an SST. Read-visible to Get. The
    // flush thread drains these in FIFO order; once flushOne() succeeds, the
    // entry is erased here.
    std::vector<std::shared_ptr<MemTable>> immutable_list_;
    std::atomic<uint64_t>     seq_;
    std::mutex                write_mutex_;

    // Group-commit state. All accesses under write_mutex_.
    std::condition_variable group_cv_;
    std::vector<std::shared_ptr<GroupEntry>> group_entries_;
    bool                     group_leader_active_ = false;

    // Background-flush state.
    std::mutex               flush_mu_;
    std::condition_variable  flush_cv_;
    std::thread              flush_thread_;
    std::atomic<bool>        flush_running_{true};
    std::atomic<int>         flush_inflight_{0};
    std::deque<FlushEntry>   flush_queue_;

    Version                   version_;
    std::unique_ptr<BlockCache> block_cache_;
    std::unique_ptr<TableCache> table_cache_;
    std::unique_ptr<CompactionManager> compaction_mgr_;
};

}  // namespace core
}  // namespace minikv
