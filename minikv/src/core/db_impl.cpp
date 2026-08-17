#include "core/db_impl.h"
#include "core/sstable_builder.h"
#include "core/sstable_reader.h"
#include "core/sstable_iterator.h"
#include "core/merging_iterator.h"
#include "core/compression.h"
#include "core/internal_key.h"
#include "utils/coding.h"
#include <dirent.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>
#include <cctype>
#include <cstdlib>
#include <cstring>
#include <iostream>
#include <algorithm>
#include <memory>
#include <utility>
#include <vector>

namespace minikv {
namespace core {

DBImpl::DBImpl(const Options& options) : options_(options), seq_(0) {
    db_path_ = options_.db_path;
    if (db_path_.empty()) db_path_ = "./minikv_data";
    ::mkdir(db_path_.c_str(), 0755);
    for (int i = 0; i <= options_.max_level; ++i) {
        std::string levelDir = db_path_ + "/level-" + std::to_string(i);
        ::mkdir(levelDir.c_str(), 0755);
    }
}

DBImpl::~DBImpl() {
    if (wal_) wal_->sync();
    if (compaction_mgr_) compaction_mgr_->stop();
}

Status DBImpl::open(const Options& options, std::unique_ptr<DB>* dbptr) {
    auto impl = std::make_unique<DBImpl>(options);
    Status s = impl->recover();
    if (!s.ok()) return s;
    impl->compaction_mgr_ = std::make_unique<CompactionManager>(&impl->version_, impl->db_path_);
    impl->compaction_mgr_->start();
    *dbptr = std::move(impl);
    return Status::Ok();
}

Status DBImpl::recover() {
    manifest_ = std::make_unique<Manifest>(db_path_);
    Status ms = manifest_->open();
    if (!ms.ok()) return ms;
    version_.setManifest(manifest_.get());
    version_.restoreFrom(manifest_->levels());

    // Compact Manifest: archive old append log as MANIFEST.bak and rewrite a
    // fresh MANIFEST that lists every live SST as kAdd (full snapshot).
    // This is the safe alternative to empty kReset — old SSTs are re-recorded
    // before any future edits append. Do NOT append empty kReset here.
    Status snap = manifest_->rewriteSnapshot();
    if (!snap.ok()) return snap;

    memtable_ = std::make_shared<MemTable>(options_.memtable_size);

    auto wals = listWalFiles();
    for (const auto& [num, path] : wals) {
        version_.ensureNextFileNumberAtLeast(num + 1);
        Status rs = replayWalFile(path);
        if (!rs.ok()) return rs;
    }

    if (wals.empty()) {
        Status os = openWal(version_.nextFileNumber());
        if (!os.ok()) return os;
    } else {
        // Continue appending to the newest WAL; older files stay until next flush.
        current_wal_path_ = wals.back().second;
        wal_ = std::make_unique<WAL>(current_wal_path_);
        obsolete_wal_paths_.clear();
        for (size_t i = 0; i + 1 < wals.size(); ++i) {
            obsolete_wal_paths_.push_back(wals[i].second);
        }
    }
    return Status::Ok();
}

std::string DBImpl::makeWalPath(uint64_t file_number) const {
    return db_path_ + "/wal-" + std::to_string(file_number) + ".log";
}

std::vector<std::pair<uint64_t, std::string>> DBImpl::listWalFiles() const {
    std::vector<std::pair<uint64_t, std::string>> out;

    // Legacy single-file WAL (pre multi-WAL): treat as generation 0.
    std::string legacy = db_path_ + "/wal.log";
    struct stat st;
    if (::stat(legacy.c_str(), &st) == 0 && S_ISREG(st.st_mode) && st.st_size > 0) {
        out.emplace_back(0, legacy);
    }

    DIR* dir = ::opendir(db_path_.c_str());
    if (dir) {
        while (dirent* ent = ::readdir(dir)) {
            const char* name = ent->d_name;
            // Match wal-<digits>.log
            if (std::strncmp(name, "wal-", 4) != 0) continue;
            const char* p = name + 4;
            if (!std::isdigit(static_cast<unsigned char>(*p))) continue;
            char* end = nullptr;
            unsigned long long num = std::strtoull(p, &end, 10);
            if (!end || std::strcmp(end, ".log") != 0) continue;
            out.emplace_back(static_cast<uint64_t>(num), db_path_ + "/" + name);
        }
        ::closedir(dir);
    }

    std::sort(out.begin(), out.end(),
              [](const auto& a, const auto& b) { return a.first < b.first; });
    return out;
}

Status DBImpl::openWal(uint64_t file_number) {
    version_.ensureNextFileNumberAtLeast(file_number + 1);
    current_wal_path_ = makeWalPath(file_number);
    wal_ = std::make_unique<WAL>(current_wal_path_);
    return Status::Ok();
}

Status DBImpl::rotateWal() {
    // Create the new file first; only then retire the old path.
    uint64_t n = version_.nextFileNumber();
    std::string new_path = makeWalPath(n);
    version_.ensureNextFileNumberAtLeast(n + 1);
    auto new_wal = std::make_unique<WAL>(new_path);

    if (wal_) {
        (void)wal_->sync();
    }
    if (!current_wal_path_.empty()) {
        obsolete_wal_paths_.push_back(current_wal_path_);
    }
    wal_ = std::move(new_wal);
    current_wal_path_ = std::move(new_path);
    return Status::Ok();
}

Status DBImpl::replayWalFile(const std::string& path) {
    WAL wal(path);
    auto records = wal.replay();
    for (const auto& record : records) {
        const char* p = record.data();
        const char* end = record.data() + record.size();
        while (p < end) {
            uint8_t type = static_cast<uint8_t>(*p++);
            uint32_t keyLen = utils::decodeFixed32(p); p += 4;
            uint32_t valLen = utils::decodeFixed32(p); p += 4;
            Slice key(p, keyLen); p += keyLen;
            Slice value(p, valLen); p += valLen;
            bool isDelete = (type == 2);
            memtable_->put(key, value, seq_, isDelete);
            seq_++;
        }
    }
    return Status::Ok();
}

Status DBImpl::put(const WriteOptions& opts, const Slice& key, const Slice& value) {
    WriteBatch batch;
    batch.put(key, value);
    return write(opts, batch);
}

Status DBImpl::del(const WriteOptions& opts, const Slice& key) {
    WriteBatch batch;
    batch.del(key);
    return write(opts, batch);
}

Status DBImpl::write(const WriteOptions& opts, const WriteBatch& batch) {
    std::lock_guard<std::mutex> lock(write_mutex_);
    // Reserve seq numbers up front. If WAL fails we may leave gaps — that is OK.
    const size_t n = batch.count();
    uint64_t baseSeq = seq_.fetch_add(n);

    // 1) Write-Ahead Log first: durable on disk before MemTable is visible.
    //    Crash after WAL succeeds / before MemTable apply → recover() replays WAL.
    if (wal_) {
        std::string data;
        data.reserve(n * 16);
        for (const auto& op : batch.ops()) {
            data.push_back(static_cast<char>(static_cast<uint8_t>(op.type)));
            char lenBuf[4];
            utils::encodeFixed32(lenBuf, static_cast<uint32_t>(op.key.size()));
            data.append(lenBuf, 4);
            utils::encodeFixed32(lenBuf, static_cast<uint32_t>(op.value.size()));
            data.append(lenBuf, 4);
            data.append(op.key);
            data.append(op.value);
        }
        Status ws = wal_->append(Slice(data));
        if (!ws.ok()) return ws;
        if (options_.wal_sync && opts.sync) {
            Status s = wal_->sync();
            if (!s.ok()) return s;
        }
    }

    // 2) Apply to MemTable only after WAL succeeded (or WAL disabled).
    uint64_t currentSeq = baseSeq;
    for (const auto& op : batch.ops()) {
        uint64_t opSeq = ++currentSeq;
        bool isDel = (op.type == BatchOpType::kDelete);
        memtable_->put(Slice(op.key), Slice(op.value), opSeq, isDel);
    }
    maybeFlush();
    return Status::Ok();
}

Status DBImpl::get(const ReadOptions& opts, const Slice& key, std::string* value) {
    (void)opts;
    std::shared_ptr<MemTable> mem, imm;
    {
        // Copy shared_ptr (same control block, use_count +1) then unlock.
        // Lookup is outside write_mutex_ so flush move/reset cannot UAF.
        // Never reconstruct via shared_ptr<MemTable>(memtable_.get()).
        std::lock_guard<std::mutex> lock(write_mutex_);
        mem = memtable_;
        imm = immutable_memtable_;
    }
    const uint64_t snapshot_seq = seq_.load();
    if (mem) {
        auto result = mem->get(key, snapshot_seq);
        if (result) {
            *value = std::move(*result);
            return Status::Ok();
        }
    }
    if (imm) {
        auto result = imm->get(key, snapshot_seq);
        if (result) {
            *value = std::move(*result);
            return Status::Ok();
        }
    }
    for (int level = 0; level <= options_.max_level; ++level) {
        auto files = version_.getLevelFiles(level);
        for (const auto& path : files) {
            auto reader = SSTableReader::open(path);
            if (!reader) continue;
            auto r = reader->get(key);
            if (r) {
                *value = std::move(*r);
                return Status::Ok();
            }
        }
    }
    return Status::NotFound();
}

void DBImpl::maybeFlush() {
    if (memtable_->shouldFlush()) {
        immutable_memtable_ = std::move(memtable_);
        // New MemTable gets a brand-new WAL file (no truncate/reopen of the old one).
        Status rs = rotateWal();
        if (!rs.ok()) {
            // Best-effort: keep serving with old WAL if rotate failed (should be rare).
            std::cerr << "rotateWal failed: " << rs.message() << std::endl;
        }
        memtable_ = std::make_shared<MemTable>(options_.memtable_size);
        flushMemTable();
    }
}

Status DBImpl::flushMemTable() {
    if (!immutable_memtable_ || immutable_memtable_->empty()) {
        immutable_memtable_.reset();
        // Empty generation: old WAL has nothing to replay; drop retired files.
        for (const auto& path : obsolete_wal_paths_) {
            ::unlink(path.c_str());
        }
        obsolete_wal_paths_.clear();
        return Status::Ok();
    }
    auto flushing = immutable_memtable_;
    auto entries = flushing->entries();
    std::string filePath = db_path_ + "/level-0/" +
        std::to_string(version_.nextFileNumber()) + ".sst";
    CompressionType ctype = static_cast<CompressionType>(options_.compression);
    SSTableBuilder builder(filePath, options_.block_size, ctype);
    for (const auto& entry : entries) {
        Slice ik(entry.internal_key);
        Slice uk = InternalKeyUserKey(ik);
        builder.add(ik, uk, Slice(entry.value));
    }
    builder.finish();
    version_.addLevelFile(0, filePath);
    immutable_memtable_.reset();

    // Data now durable in SST: drop WAL files that belonged to this generation.
    for (const auto& path : obsolete_wal_paths_) {
        ::unlink(path.c_str());
    }
    obsolete_wal_paths_.clear();
    return Status::Ok();
}

std::unique_ptr<Iterator> DBImpl::newIterator(const ReadOptions& opts) {
    (void)opts;
    std::vector<std::unique_ptr<Iterator>> children;

    std::shared_ptr<MemTable> mem, imm;
    {
        std::lock_guard<std::mutex> lock(write_mutex_);
        mem = memtable_;
        imm = immutable_memtable_;
    }

    if (mem) {
        auto live = mem->entries();
        auto it = std::make_unique<MemTableIterator>(std::move(live));
        it->seekToFirst();
        children.push_back(std::move(it));
    }
    if (imm) {
        auto imm_entries = imm->entries();
        auto it = std::make_unique<MemTableIterator>(std::move(imm_entries));
        it->seekToFirst();
        children.push_back(std::move(it));
    }

    for (int level = 0; level <= options_.max_level; ++level) {
        for (const auto& path : version_.getLevelFiles(level)) {
            auto reader = SSTableReader::open(path);
            if (!reader) continue;
            std::shared_ptr<SSTableReader> shared(std::move(reader));
            auto it = std::make_unique<SSTableIterator>(std::move(shared));
            it->seekToFirst();
            children.push_back(std::move(it));
        }
    }

    auto merged = std::make_unique<MergingIterator>(std::move(children));
    merged->seekToFirst();
    return merged;
}

void DBImpl::compact() {
    if (compaction_mgr_) compaction_mgr_->triggerCompaction();
}

}  // namespace core

Status DB::open(const Options& options, std::unique_ptr<DB>* dbptr) {
    return core::DBImpl::open(options, dbptr);
}

}  // namespace minikv