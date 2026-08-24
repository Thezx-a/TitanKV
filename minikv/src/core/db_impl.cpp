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
#include <unordered_set>
#include <chrono>
#include <thread>

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
    // Background flush thread. It waits on flush_cv_ until maybeFlush
    // enqueues a frozen MemTable, then runs the heavy SSTable build outside
    // any write lock. Started here (rather than in open()) because flushLoop
    // only touches flush_queue_ / immutable_list_, which are empty before
    // recover() so the wait is a no-op until needed.
    flush_thread_ = std::thread(&DBImpl::flushLoop, this);
    if (options_.lru_cache_capacity > 0) {
        block_cache_ = std::make_unique<BlockCache>(options_.lru_cache_capacity);
    }
}

DBImpl::~DBImpl() {
    if (wal_) wal_->sync();
    if (compaction_mgr_) compaction_mgr_->stop();

    // Stop background flush thread. Wake it so it observes flush_running_=false,
    // then join so we don't drop a MemTable mid-build (which would lose data
    // still in memory but not yet written to an SST).
    flush_running_.store(false);
    flush_cv_.notify_all();
    if (flush_thread_.joinable()) flush_thread_.join();
    waitFlush();
}

Status DBImpl::open(const Options& options, std::unique_ptr<DB>* dbptr) {
    auto impl = std::make_unique<DBImpl>(options);
    Status s = impl->recover();
    if (!s.ok()) return s;
    impl->compaction_mgr_ = std::make_unique<CompactionManager>(
        &impl->version_, impl->db_path_, impl->options_.block_size,
        impl->options_.max_level, impl->options_.level0_compaction_trigger,
        impl->block_cache_.get());
    impl->compaction_mgr_->start();
    *dbptr = std::move(impl);
    return Status::Ok();
}

Status DBImpl::purgeOrphanSSTables() {
    // Live-SST path set is the source of truth. Any .sst on disk not in
    // manifest_->levels() is an orphan left by a crashed flush/compaction
    // (e.g. SST file written but recordAddFile never appended to MANIFEST).
    std::unordered_set<std::string> live_paths;
    const auto& levels = manifest_->levels();
    for (const auto& level_vec : levels) {
        for (const auto& meta : level_vec) {
            live_paths.insert(meta.path);
        }
    }

    // Walk every level-<i> directory and unlink anything not in live set.
    // opendir/unlink failures are tolerated (warn + continue) so a stuck
    // orphan or a missing dir cannot block DB open.
    size_t purged = 0;
    for (int level = 0; level <= options_.max_level; ++level) {
        std::string dir_path = db_path_ + "/level-" + std::to_string(level);
        DIR* dir = ::opendir(dir_path.c_str());
        if (!dir) continue;  // dir not created yet (e.g. fresh DB)

        struct dirent* ent;
        while ((ent = ::readdir(dir)) != nullptr) {
            if (ent->d_name[0] == '.') continue;  // skip . / ..
            std::string full = dir_path + "/" + ent->d_name;
            // Only consider .sst files; ignore anything else (e.g. tmp files).
            if (full.size() < 4 ||
                full.compare(full.size() - 4, 4, ".sst") != 0) {
                continue;
            }
            if (live_paths.count(full) > 0) continue;  // live, keep

            if (::unlink(full.c_str()) == 0) {
                ++purged;
                std::cerr << "[INFO] purged orphan SST: " << full << std::endl;
            } else {
                std::cerr << "[WARN] unlink orphan failed: " << full
                          << " errno=" << std::strerror(errno) << std::endl;
            }
        }
        ::closedir(dir);
    }

    if (purged > 0) {
        std::cerr << "[INFO] purgeOrphanSSTables: removed " << purged
                  << " orphan file(s)" << std::endl;
    }
    return Status::Ok();
}

Status DBImpl::recover() {
    manifest_ = std::make_unique<Manifest>(db_path_);
    Status ms = manifest_->open();
    if (!ms.ok()) return ms;
    version_.setManifest(manifest_.get());
    version_.restoreFrom(manifest_->levels());

    // Garbage-collect orphan SSTables BEFORE rewriting MANIFEST snapshot:
    //   (1) free disk space held by crash-left orphans (e.g. a flush that wrote
    //       the .sst file but crashed before recordAddFile was appended);
    //   (2) leave room for the new MANIFEST that follows.
    // Orphans are detected by listing level-*/ dirs and diffing against
    // manifest_->levels(); files whose path is not in the live set are
    // unlinked. unlink failures are non-fatal: we warn and continue so a
    // single stuck orphan cannot block DB open.
    Status purge = purgeOrphanSSTables();
    if (!purge.ok()) {
        std::cerr << "[WARN] purge orphan SSTables failed: " << purge.message()
                  << " (continuing recovery)" << std::endl;
    }

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

// ---------------------------------------------------------------------------
// Group Commit entry point.
// ---------------------------------------------------------------------------
// First caller (group_leader_active_ == false) becomes the leader: it enqueues
// its own batch, then waits up to kGroupCommitTimeout for up to kMaxGroupSize
// followers to join. Concurrent callers see group_leader_active_ == true, push
// their own GroupEntry into the shared group_entries_ queue, and wait on
// group_cv_ until the leader sets their entry->done.
//
// After the group is formed, the leader still holds write_mutex_ (followers are
// asleep inside cv_.wait(), so the lock is theirs to use) and hands the queue to
// doWriteGroup(), which:
//   1) reserves seq numbers for the whole group atomically (seq_.fetch_add per
//      batch) so WAL byte order matches seq order;
//   2) builds ONE concatenated WAL buffer and issues ONE wal_->append() call,
//      then ONE fdatasync (only if any entry in the group had opts.sync);
//   3) applies every batch to the MemTable under its own reserved seq range;
//   4) calls maybeFlush() once.
// The leader then marks every entry done and notify_all()s the followers.
// ---------------------------------------------------------------------------
Status DBImpl::write(const WriteOptions& opts, const WriteBatch& batch) {
    auto entry = std::make_shared<GroupEntry>();
    entry->batch = batch;
    entry->opts  = opts;

    std::unique_lock<std::mutex> lock(write_mutex_);

    if (group_leader_active_) {
        // Follower: join the in-flight group. The leader will process our
        // entry together with its own and signal us via entry->done.
        group_entries_.push_back(entry);
        group_cv_.wait(lock, [&] { return entry->done; });
        return entry->status;
    }

    // Leader: form a group.
    group_leader_active_ = true;
    group_entries_.push_back(entry);

    // Wait until either enough followers join or the timeout fires.
    // The lock is released while waiting, so followers can enqueue themselves.
    auto deadline = std::chrono::steady_clock::now() + kGroupCommitTimeout;
    group_cv_.wait_until(lock, deadline, [&] {
        return group_entries_.size() >= kMaxGroupSize;
    });

    // Snapshot the group out from under any future writer. After this swap,
    // group_entries_ is empty and group_leader_active_ is false again, so a
    // writer that arrives while doWriteGroup runs will become the next leader
    // (it just has to wait on write_mutex_ first).
    std::vector<std::shared_ptr<GroupEntry>> group;
    group.swap(group_entries_);
    group_leader_active_ = false;

    // doWriteGroup keeps holding write_mutex_ (we never released it after
    // wait_until returned). Followers wake only after we set done=true below.
    Status s = doWriteGroup(group);

    for (auto& e : group) {
        e->status = s;
        e->done   = true;
    }
    group_cv_.notify_all();

    return s;
}

// Execute a formed group under write_mutex_. See contract in db_impl.h.
Status DBImpl::doWriteGroup(const std::vector<std::shared_ptr<GroupEntry>>& group) {
    if (group.empty()) return Status::Ok();

    // Conservative sync policy: if ANY entry wanted sync, the whole group syncs.
    bool need_sync = false;
    for (const auto& e : group) {
        if (e->opts.sync) { need_sync = true; break; }
    }

    // Reserve seq numbers up front, one atomic fetch_add per batch. The order
    // of fetch_add calls matches the order entries were enqueued, which is the
    // same order they will be appended to WAL below — so WAL byte order == seq
    // order, which is what recover() relies on.
    std::vector<uint64_t> base_seqs;
    base_seqs.reserve(group.size());
    for (const auto& e : group) {
        base_seqs.push_back(seq_.fetch_add(e->batch.count()));
    }

    // 1) Write-Ahead Log first: durable on disk before MemTable is visible.
    //    Crash after WAL succeeds / before MemTable apply -> recover() replays WAL.
    if (wal_) {
        std::string data;
        size_t reserve_bytes = 0;
        for (const auto& e : group) reserve_bytes += e->batch.count() * 16;
        data.reserve(reserve_bytes);

        for (const auto& e : group) {
            for (const auto& op : e->batch.ops()) {
                data.push_back(static_cast<char>(static_cast<uint8_t>(op.type)));
                char lenBuf[4];
                utils::encodeFixed32(lenBuf, static_cast<uint32_t>(op.key.size()));
                data.append(lenBuf, 4);
                utils::encodeFixed32(lenBuf, static_cast<uint32_t>(op.value.size()));
                data.append(lenBuf, 4);
                data.append(op.key);
                data.append(op.value);
            }
        }
        Status ws = wal_->append(Slice(data));
        if (!ws.ok()) return ws;
        if (options_.wal_sync && need_sync) {
            Status s = wal_->sync();
            if (!s.ok()) return s;
        }
    }

    // 2) Apply to MemTable only after WAL succeeded (or WAL disabled).
    //    Each batch uses the seq range it reserved above (++currentSeq mirrors
    //    the original single-batch path: first op gets baseSeq+1, then +2, ...).
    for (size_t i = 0; i < group.size(); ++i) {
        uint64_t currentSeq = base_seqs[i];
        for (const auto& op : group[i]->batch.ops()) {
            uint64_t opSeq = ++currentSeq;
            bool isDel = (op.type == BatchOpType::kDelete);
            memtable_->put(Slice(op.key), Slice(op.value), opSeq, isDel);
        }
    }

    maybeFlush();
    return Status::Ok();
}

Status DBImpl::get(const ReadOptions& opts, const Slice& key, std::string* value) {
    (void)opts;
    std::shared_ptr<MemTable> mem;
    std::vector<std::shared_ptr<MemTable>> imms;
    {
        // Snapshot memtable_ + the whole immutable_list_ under the lock, then
        // release. Iteration below is lock-free; shared_ptr refcounts keep
        // every MemTable alive even if flushOne() erases from immutable_list_
        // concurrently. immutable_list_ is ordered FIFO (oldest at front), so
        // we iterate in reverse: newest version first, mirroring the seq
        // ordering the LSM-Tree read path requires.
        std::lock_guard<std::mutex> lock(write_mutex_);
        mem = memtable_;
        imms = immutable_list_;
    }
    const uint64_t snapshot_seq = seq_.load();
    if (mem) {
        auto result = mem->get(key, snapshot_seq);
        if (result) {
            *value = std::move(*result);
            return Status::Ok();
        }
    }
    for (auto it = imms.rbegin(); it != imms.rend(); ++it) {
        auto result = (*it)->get(key, snapshot_seq);
        if (result) {
            *value = std::move(*result);
            return Status::Ok();
        }
    }
    for (int level = 0; level <= options_.max_level; ++level) {
        auto files = version_.getLevelFiles(level);
        for (const auto& path : files) {
            auto reader = SSTableReader::open(path, block_cache_.get());
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

// ---------------------------------------------------------------------------
// Async flush handoff.
// ---------------------------------------------------------------------------
// Cheap step (under write_mutex_): freeze the current MemTable into
// immutable_list_, rotate to a new WAL file, install a fresh empty MemTable
// so writes can proceed immediately, snapshot the WAL files that belong to
// this generation, then enqueue the (memtable, wals) pair on flush_queue_.
// notify flush_cv_ and return — write_mutex_ is released right here. The
// heavy SSTable build happens on the flush thread, which takes no write lock
// during IO and only briefly reacquires write_mutex_ at the end to erase the
// finished entry from immutable_list_.
// ---------------------------------------------------------------------------
void DBImpl::maybeFlush() {
    if (!memtable_->shouldFlush()) return;

    // Caller (doWriteGroup) already holds write_mutex_. We must NOT
    // re-acquire it here — std::mutex is non-recursive and would self-deadlock.
    // (immutable_list_, memtable_, obsolete_wal_paths_ are all under
    // write_mutex_; safe to touch directly.)
    if (!memtable_->shouldFlush()) return;

    std::shared_ptr<MemTable> to_flush = std::move(memtable_);
    // New MemTable gets a brand-new WAL file (no truncate/reopen of the old one).
    Status rs = rotateWal();
    if (!rs.ok()) {
        // Best-effort: keep serving with old WAL if rotate failed (should be rare).
        std::cerr << "rotateWal failed: " << rs.message() << std::endl;
    }
    memtable_ = std::make_shared<MemTable>(options_.memtable_size);

    // Snapshot the WAL files whose data is fully covered by `to_flush`.
    // rotateWal() above pushed the previous current_wal_path_ into
    // obsolete_wal_paths_; we hand them to the flush thread which unlinks
    // them ONLY after the SST is durable on disk.
    std::vector<std::string> wals;
    wals.swap(obsolete_wal_paths_);

    // Make the frozen MemTable read-visible to Get/newIterator while the
    // flush thread is working on it. shared_ptr refcounts keep it alive
    // even after erase from immutable_list_ for any Get that snapshotted it.
    immutable_list_.push_back(to_flush);

    // Hand off to the flush thread. Heavy IO happens off the write path.
    {
        std::lock_guard<std::mutex> lk(flush_mu_);
        flush_queue_.push_back(FlushEntry{std::move(to_flush), std::move(wals)});
    }
    flush_cv_.notify_one();
}

// Flush thread main loop. Runs until flush_running_ is false AND the queue
// is drained (so destructor doesn't drop in-flight data).
void DBImpl::flushLoop() {
    while (true) {
        FlushEntry entry;
        {
            std::unique_lock<std::mutex> lk(flush_mu_);
            flush_cv_.wait(lk, [&] {
                return !flush_queue_.empty() || !flush_running_.load();
            });
            if (flush_queue_.empty() && !flush_running_.load()) return;
            // Drain in FIFO order: oldest MemTable first, matching the order
            // they were frozen by maybeFlush. L0 SSTables produced earlier in
            // this loop end up with smaller file numbers and thus are picked
            // up by reads/compaction in the right time order.
            entry = std::move(flush_queue_.front());
            flush_queue_.pop_front();
            flush_inflight_.fetch_add(1);
        }
        // Heavy IO OUTSIDE any lock.
        Status s = flushOne(entry.memtable, entry.wals);
        flush_inflight_.fetch_sub(1);
        flush_cv_.notify_all();
        if (!s.ok()) {
            std::cerr << "[WARN] background flush failed: " << s.message()
                      << " (will retry)" << std::endl;
            // Re-queue for retry (FIFO preserves order).
            std::lock_guard<std::mutex> lk(flush_mu_);
            flush_queue_.push_front(std::move(entry));
            std::this_thread::sleep_for(std::chrono::milliseconds(200));
        }
    }
}

// Heavy work for one MemTable generation. No lock held during IO; only a
// brief write_mutex_ at the end to erase from immutable_list_.
Status DBImpl::flushOne(const std::shared_ptr<MemTable>& flushing,
                        const std::vector<std::string>& wals) {
    if (!flushing || flushing->empty()) {
        // Empty generation: old WAL has nothing to replay; just drop retired files.
        for (const auto& path : wals) ::unlink(path.c_str());
        std::lock_guard<std::mutex> lock(write_mutex_);
        auto it = std::find(immutable_list_.begin(), immutable_list_.end(), flushing);
        if (it != immutable_list_.end()) immutable_list_.erase(it);
        return Status::Ok();
    }

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

    // SST is durable on disk now: it is safe to (1) drop the MemTable from
    // immutable_list_ (reads will fall through to the SST) and (2) unlink the
    // WAL files for this generation (recover() will not need to replay them).
    {
        std::lock_guard<std::mutex> lock(write_mutex_);
        auto it = std::find(immutable_list_.begin(), immutable_list_.end(), flushing);
        if (it != immutable_list_.end()) immutable_list_.erase(it);
    }

    for (const auto& path : wals) {
        if (::unlink(path.c_str()) != 0) {
            // Non-fatal: log and continue. A missing WAL just means there's
            // nothing to replay — which is correct since the SST is durable.
            std::cerr << "[WARN] unlink WAL failed: " << path
                      << " errno=" << std::strerror(errno) << std::endl;
        }
    }
    return Status::Ok();
}

std::unique_ptr<Iterator> DBImpl::newIterator(const ReadOptions& opts) {
    (void)opts;
    std::vector<std::unique_ptr<Iterator>> children;

    std::shared_ptr<MemTable> mem;
    std::vector<std::shared_ptr<MemTable>> imms;
    {
        std::lock_guard<std::mutex> lock(write_mutex_);
        mem = memtable_;
        imms = immutable_list_;
    }

    if (mem) {
        auto live = mem->entries();
        auto it = std::make_unique<MemTableIterator>(std::move(live));
        it->seekToFirst();
        children.push_back(std::move(it));
    }
    // Reverse iterate (newest first) so MergingIterator sees higher-seq
    // versions before lower-seq ones from older frozen MemTables.
    for (auto it_imm = imms.rbegin(); it_imm != imms.rend(); ++it_imm) {
        auto imm_entries = (*it_imm)->entries();
        auto it = std::make_unique<MemTableIterator>(std::move(imm_entries));
        it->seekToFirst();
        children.push_back(std::move(it));
    }

    for (int level = 0; level <= options_.max_level; ++level) {
        for (const auto& path : version_.getLevelFiles(level)) {
            auto reader = SSTableReader::open(path, block_cache_.get());
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

void DBImpl::waitFlush() {
    for (;;) {
        {
            std::lock_guard<std::mutex> lk(flush_mu_);
            if (flush_queue_.empty() && flush_inflight_.load() == 0) break;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(2));
    }
}

}  // namespace core

Status DB::open(const Options& options, std::unique_ptr<DB>* dbptr) {
    return core::DBImpl::open(options, dbptr);
}

}  // namespace minikv