#include "core/compaction.h"
#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/sstable_iterator.h"
#include "core/sstable_reader.h"
#include <sys/stat.h>
#include <unistd.h>
#include <algorithm>
#include <chrono>
#include <iostream>
#include <map>
#include <vector>

namespace minikv {
namespace core {

CompactionManager::CompactionManager(Version* version, const std::string& db_path,
                                     size_t block_size, int max_level,
                                     size_t l0_trigger, BlockCache* block_cache)
    : version_(version),
      db_path_(db_path),
      block_size_(block_size),
      max_level_(max_level),
      l0_trigger_(l0_trigger),
      block_cache_(block_cache),
      running_(false),
      triggered_(false) {}

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

void CompactionManager::invalidateCache(const std::vector<std::string>& paths) {
    if (!block_cache_) return;
    for (const auto& p : paths) block_cache_->invalidatePath(p);
}

void CompactionManager::compactionLoop() {
    while (running_) {
        bool did_work = false;
        if (triggered_ || version_->shouldCompactL0(l0_trigger_)) {
            triggered_ = false;
            Status s = compactL0();
            if (!s.ok()) {
                ++compaction_failures_;
                std::cerr << "Compaction L0 failed: " << s.message() << std::endl;
                triggered_ = true;  // retry on next poll
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
                    std::cerr << "Compaction L" << lvl << " failed: " << s.message()
                              << std::endl;
                    triggered_ = true;
                } else {
                    compaction_failures_ = 0;
                    did_work = true;
                }
            }
        }
        (void)did_work;
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
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

    std::vector<std::pair<std::string, std::string>> all;
    for (const auto& path : src_files) {
        auto reader = SSTableReader::open(path, block_cache_);
        if (!reader) continue;
        std::shared_ptr<SSTableReader> shared(std::move(reader));
        SSTableIterator it(shared);
        it.seekToFirst();
        while (it.valid()) {
            all.emplace_back(it.key().toString(), it.value().toString());
            it.next();
        }
        if (!it.status().ok()) return it.status();
    }

    std::sort(all.begin(), all.end(), [](const auto& a, const auto& b) {
        return InternalKeyCompare(Slice(a.first), Slice(b.first)) < 0;
    });

    std::string dst_dir = db_path_ + "/level-" + std::to_string(dst_level);
    ::mkdir(dst_dir.c_str(), 0755);

    std::string newFile =
        dst_dir + "/" + std::to_string(version_->nextFileNumber()) + ".sst";
    SSTableBuilder builder(newFile, block_size_);

    std::string last_user;
    size_t kept = 0;
    for (const auto& kv : all) {
        Slice ik(kv.first);
        Slice uk = InternalKeyUserKey(ik);
        std::string uks = uk.toString();
        if (uks == last_user) continue;
        last_user = uks;
        // Drop tombstones when merging into a deeper level (no older versions in input).
        if (InternalKeyType(ik) == ValueType::kDeletion) continue;
        Status s = builder.add(ik, uk, Slice(kv.second));
        if (!s.ok()) return s;
        ++kept;
    }

    Status fs = builder.finish();
    if (!fs.ok()) {
        ::unlink(newFile.c_str());
        return fs;
    }

    invalidateCache(src_files);

    if (kept == 0) {
        ::unlink(newFile.c_str());
        version_->removeLevelFiles(src_level, src_files);
        for (const auto& path : src_files) ::unlink(path.c_str());
        return Status::Ok();
    }

    version_->addLevelFile(dst_level, newFile);
    version_->removeLevelFiles(src_level, src_files);
    for (const auto& path : src_files) ::unlink(path.c_str());

    return Status::Ok();
}

}  // namespace core
}  // namespace minikv
