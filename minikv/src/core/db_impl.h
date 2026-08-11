#pragma once
#include <atomic>
#include <memory>
#include <mutex>
#include <string>
#include "core/compaction.h"
#include "core/manifest.h"
#include "core/memtable.h"
#include "core/memtable_iterator.h"
#include "core/version.h"
#include "core/wal.h"
#include "minikv/db.h"
#include "minikv/options.h"

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

private:
    void maybeFlush();
    Status flushMemTable();
    Status recover();

    // One MemTable generation <-> one WAL file (wal-<N>.log). Flush deletes old files.
    std::string makeWalPath(uint64_t file_number) const;
    std::vector<std::pair<uint64_t, std::string>> listWalFiles() const;
    Status openWal(uint64_t file_number);
    Status rotateWal();
    Status replayWalFile(const std::string& path);

    Options options_;
    std::string db_path_;
    std::unique_ptr<WAL>      wal_;
    std::string               current_wal_path_;
    // WALs whose data is fully covered by the memtable about to flush / just flushed.
    std::vector<std::string>  obsolete_wal_paths_;
    std::unique_ptr<Manifest> manifest_;
    std::unique_ptr<MemTable> memtable_;
    std::unique_ptr<MemTable> immutable_memtable_;
    std::atomic<uint64_t>     seq_;
    std::mutex                write_mutex_;
    Version                   version_;
    std::unique_ptr<CompactionManager> compaction_mgr_;
};

}  // namespace core
}  // namespace minikv