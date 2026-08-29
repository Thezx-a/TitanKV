#pragma once
#include <memory>
#include <string>
#include "minikv/options.h"
#include "minikv/status.h"
#include "minikv/slice.h"
#include "minikv/write_batch.h"
#include "minikv/iterator.h"

namespace minikv {

class DB {
public:
    static Status open(const Options& options, std::unique_ptr<DB>* dbptr);
    virtual ~DB() = default;

    virtual Status put(const WriteOptions& opts, const Slice& key, const Slice& value) = 0;
    virtual Status get(const ReadOptions& opts, const Slice& key, std::string* value) = 0;
    virtual Status del(const WriteOptions& opts, const Slice& key) = 0;
    virtual Status write(const WriteOptions& opts, const WriteBatch& batch) = 0;
    // Delete all user keys in half-open range [start, end). Empty start/end = unbounded.
    // Implementation batches tombstones (<=10000 per WriteBatch). Best-effort under concurrent writes.
    virtual Status deleteRange(const WriteOptions& opts, const Slice& start, const Slice& end) = 0;
    virtual std::unique_ptr<Iterator> newIterator(const ReadOptions& opts) = 0;
    virtual void compact() = 0;
    // Block until all pending MemTable flushes finish (tests + graceful shutdown).
    virtual void waitFlush() = 0;
    // Block until background compaction has drained triggered work and no
    // level currently exceeds its file-count trigger (tests + admin).
    virtual void waitCompaction() = 0;
};

}  // namespace minikv
