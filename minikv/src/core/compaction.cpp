#include "core/compaction.h"
#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/sstable_iterator.h"
#include "core/sstable_reader.h"
#include "core/merging_iterator.h"
#include "minikv/iterator.h"
#include "utils/env.h"
#include "utils/metrics.h"
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>
#include <algorithm>
#include <chrono>
#include <iostream>
#include <map>
#include <vector>

namespace minikv {
namespace core {

namespace {
void unlinkSstAndBloom(const std::string& sst_path) {
    ::unlink(sst_path.c_str());
    ::unlink((sst_path + ".bloom").c_str());
}
}  // namespace

int compactionRetryBackoffMs(int consecutive_failures) {
    if (consecutive_failures <= 1) return 100;
    int shift = consecutive_failures - 1;
    if (shift > 5) shift = 5;  // 100 << 5 = 3200
    return 100 << shift;
}

CompactionManager::CompactionManager(Version* version, const std::string& db_path,
                                     size_t block_size, int max_level,
                                     size_t l0_trigger, BlockCache* block_cache,
                                     TableCache* table_cache, int fail_inject)
    : version_(version),
      db_path_(db_path),
      block_size_(block_size),
      max_level_(max_level),
      l0_trigger_(l0_trigger),
      block_cache_(block_cache),
      table_cache_(table_cache),
      running_(false),
      triggered_(false),
      fail_inject_(fail_inject) {}

CompactionManager::~CompactionManager() { stop(); }

void CompactionManager::start() {
    running_ = true;
    compact_thread_ = std::thread([this] { compactionLoop(); });
}

void CompactionManager::stop() {
    running_ = false;
    if (compact_thread_.joinable()) compact_thread_.join();
}

void CompactionManager::triggerCompaction() { triggered_ = true; }

void CompactionManager::injectFailures(int n) {
    fail_inject_.store(n < 0 ? 0 : n, std::memory_order_relaxed);
}

bool CompactionManager::mayHaveOlderVersionsBelow(int dst_level) const {
    // Honest LevelDB-style bound: only the bottommost level can drop tombstones.
    // Range-aware drop is T2.2 — keep this hook for that upgrade.
    return dst_level < max_level_;
}

void CompactionManager::waitIdle() {
    for (int i = 0; i < 10000; ++i) {  // ~100s max at 10ms
        bool need = triggered_.load() || compacting_.load();
        if (!need && version_->shouldCompactL0(l0_trigger_)) need = true;
        for (int lvl = 1; !need && lvl < max_level_; ++lvl) {
            if (version_->shouldCompactLevel(lvl, 2)) need = true;
        }
        if (!need) return;
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
}

void CompactionManager::invalidateCache(const std::vector<std::string>& paths) {
    for (const auto& p : paths) {
        if (table_cache_) table_cache_->evict(p);
        else if (block_cache_) block_cache_->invalidatePath(p);
    }
}

void CompactionManager::compactionLoop() {
    while (running_) {
        bool did_work = false;
        int backoff_ms = 100;
        if (triggered_ || version_->shouldCompactL0(l0_trigger_)) {
            triggered_ = false;
            Status s = compactL0();
            if (!s.ok()) {
                ++compaction_failures_;
                utils::EngineMetrics::instance().compaction_failures.fetch_add(
                    1, std::memory_order_relaxed);
                std::cerr << "Compaction L0 failed: " << s.message()
                          << " (retry backoff "
                          << compactionRetryBackoffMs(compaction_failures_)
                          << "ms)" << std::endl;
                triggered_ = true;  // retry on next poll
                backoff_ms = compactionRetryBackoffMs(compaction_failures_);
            } else {
                compaction_failures_ = 0;
                did_work = true;
            }
        }
        for (int lvl = 1; lvl < max_level_; ++lvl) {
            if (version_->shouldCompactLevel(lvl, 2)) {
                Status s = compactLevel(lvl);
                if (!s.ok()) {
                    ++compaction_failures_;
                    utils::EngineMetrics::instance().compaction_failures.fetch_add(
                        1, std::memory_order_relaxed);
                    std::cerr << "Compaction L" << lvl << " failed: " << s.message()
                              << " (retry backoff "
                              << compactionRetryBackoffMs(compaction_failures_)
                              << "ms)" << std::endl;
                    triggered_ = true;
                    backoff_ms = compactionRetryBackoffMs(compaction_failures_);
                } else {
                    compaction_failures_ = 0;
                    did_work = true;
                }
            }
        }
        (void)did_work;
        std::this_thread::sleep_for(std::chrono::milliseconds(backoff_ms));
    }
}

Status CompactionManager::compactL0() {
    auto l0_files = version_->getLevelFiles(0);
    if (l0_files.empty()) return Status::Ok();
    return mergeLevelFiles(0, l0_files);
}

Status CompactionManager::compactLevel(int level) {
    if (level < 1 || level >= max_level_) return Status::Ok();
    auto files = version_->getLevelFiles(level);
    if (files.size() < 2) return Status::Ok();
    return mergeLevelFiles(level, files);
}

Status CompactionManager::mergeLevelFiles(int src_level,
                                          const std::vector<std::string>& src_files) {
    if (src_files.empty()) return Status::Ok();
    const int dst_level = src_level + 1;
    if (dst_level > max_level_) return Status::Ok();

    // E4: deterministic failure inject for retry tests (production: 0).
    {
        int left = fail_inject_.load(std::memory_order_relaxed);
        if (left > 0 &&
            fail_inject_.compare_exchange_strong(left, left - 1,
                                                 std::memory_order_relaxed)) {
            return Status::IOError("injected compaction failure");
        }
    }

    compacting_.store(true);
    struct CompactingGuard {
        std::atomic<bool>* flag;
        ~CompactingGuard() { flag->store(false); }
    } guard{&compacting_};

    // T2.1: k-way streaming merge via MergingIterator (no full-file vector).
    // L0: reverse file order so newer SST is earlier child (tie-break).
    std::vector<std::string> ordered = src_files;
    if (src_level == 0) {
        std::reverse(ordered.begin(), ordered.end());
    }

    std::vector<std::unique_ptr<::minikv::Iterator>> children;
    children.reserve(ordered.size());
    for (const auto& path : ordered) {
        std::shared_ptr<SSTableReader> reader;
        if (table_cache_) {
            reader = table_cache_->get(path);
        } else {
            auto owned = SSTableReader::open(path, block_cache_);
            if (owned) reader = std::shared_ptr<SSTableReader>(std::move(owned));
        }
        if (!reader) continue;
        children.push_back(std::make_unique<SSTableIterator>(reader));
    }
    if (children.empty()) return Status::Ok();

    MergingIterator merger(std::move(children));
    merger.seekToFirst();
    if (!merger.status().ok()) return merger.status();

    std::string dst_dir = db_path_ + "/level-" + std::to_string(dst_level);
    ::mkdir(dst_dir.c_str(), 0755);

    uint64_t file_no = version_->nextFileNumber();
    std::string final_path =
        dst_dir + "/" + std::to_string(file_no) + ".sst";
    std::string tmp_path = final_path + ".tmp";

    SSTableBuilder builder(tmp_path, block_size_);

    std::string last_user;
    size_t kept = 0;
    for (; merger.valid(); merger.next()) {
        Slice ik = merger.key();
        Slice uk = InternalKeyUserKey(ik);
        std::string uks = uk.toString();
        if (uks == last_user) continue;
        last_user = uks;
        if (InternalKeyType(ik) == ValueType::kDeletion &&
            !mayHaveOlderVersionsBelow(dst_level)) {
            continue;
        }
        Status s = builder.add(ik, uk, merger.value());
        if (!s.ok()) {
            ::unlink(tmp_path.c_str());
            ::unlink((tmp_path + ".bloom").c_str());
            return s;
        }
        ++kept;
    }
    if (!merger.status().ok()) {
        ::unlink(tmp_path.c_str());
        ::unlink((tmp_path + ".bloom").c_str());
        return merger.status();
    }

    Status fs = builder.finish();
    if (!fs.ok()) {
        ::unlink(tmp_path.c_str());
        ::unlink((tmp_path + ".bloom").c_str());
        return fs;
    }

    // Durability: fsync SST + bloom, rename, fsync directory (T2.1).
    {
        int fd = ::open(tmp_path.c_str(), O_RDONLY);
        if (fd >= 0) {
            ::fsync(fd);
            ::close(fd);
        }
        std::string bloom_tmp = tmp_path + ".bloom";
        int bfd = ::open(bloom_tmp.c_str(), O_RDONLY);
        if (bfd >= 0) {
            ::fsync(bfd);
            ::close(bfd);
        }
    }

    invalidateCache(src_files);

    if (kept == 0) {
        ::unlink(tmp_path.c_str());
        ::unlink((tmp_path + ".bloom").c_str());
        version_->removeLevelFiles(src_level, src_files);
        for (const auto& path : src_files) unlinkSstAndBloom(path);
        return Status::Ok();
    }

    if (::rename(tmp_path.c_str(), final_path.c_str()) != 0) {
        ::unlink(tmp_path.c_str());
        ::unlink((tmp_path + ".bloom").c_str());
        return Status::IOError("rename compacted SST failed");
    }
    // Bloom was written as path+".bloom" for tmp → rename sibling.
    std::string bloom_tmp = tmp_path + ".bloom";
    std::string bloom_final = final_path + ".bloom";
    if (::access(bloom_tmp.c_str(), F_OK) == 0) {
        if (::rename(bloom_tmp.c_str(), bloom_final.c_str()) != 0) {
            ::unlink(final_path.c_str());
            ::unlink(bloom_tmp.c_str());
            return Status::IOError("rename compacted bloom failed");
        }
    }
    (void)utils::fsyncDir(dst_dir);

    // Persist order: add dst → remove src → unlink sources.
    version_->addLevelFile(dst_level, final_path);
    utils::EngineMetrics::instance().compactions.fetch_add(1, std::memory_order_relaxed);
    version_->removeLevelFiles(src_level, src_files);
    for (const auto& path : src_files) unlinkSstAndBloom(path);

    return Status::Ok();
}

}  // namespace core
}  // namespace minikv
