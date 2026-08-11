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
                                     size_t block_size)
    : version_(version), db_path_(db_path), block_size_(block_size),
      running_(false), triggered_(false) {}

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

void CompactionManager::compactionLoop() {
    while (running_) {
        if (triggered_ || version_->shouldCompactL0()) {
            triggered_ = false;
            Status s = compactL0();
            if (!s.ok()) std::cerr << "Compaction failed: " << s.message() << std::endl;
        }
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
}

Status CompactionManager::compactL0() {
    auto l0_files = version_->getLevelFiles(0);
    if (l0_files.empty()) return Status::Ok();

    // Collect all (internal_key, value) from L0, then keep newest per user_key.
    // InternalKey sort: user_key asc, seq desc — first hit wins for each user_key.
    std::vector<std::pair<std::string, std::string>> all;
    for (const auto& path : l0_files) {
        auto reader = SSTableReader::open(path);
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

    std::string level1_dir = db_path_ + "/level-1";
    ::mkdir(level1_dir.c_str(), 0755);

    std::string newFile =
        level1_dir + "/" + std::to_string(version_->nextFileNumber()) + ".sst";
    SSTableBuilder builder(newFile, block_size_);

    std::string last_user;
    size_t kept = 0;
    for (const auto& kv : all) {
        Slice ik(kv.first);
        Slice uk = InternalKeyUserKey(ik);
        std::string uks = uk.toString();
        if (uks == last_user) continue;  // older version of same key
        last_user = uks;
        // Drop tombstones at L0→L1 when no older live version remains in this
        // compaction input set (newest is deletion ⇒ key is gone).
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

    // Crash-safe publish order: new SST visible in Manifest first, then drop L0,
    // then unlink. Empty output is valid (all keys deleted).
    if (kept == 0) {
        ::unlink(newFile.c_str());
        version_->removeLevelFiles(0, l0_files);
        for (const auto& path : l0_files) ::unlink(path.c_str());
        return Status::Ok();
    }

    version_->addLevelFile(1, newFile);
    version_->removeLevelFiles(0, l0_files);
    for (const auto& path : l0_files) ::unlink(path.c_str());

    return Status::Ok();
}

Status CompactionManager::compactLevel(int level) {
    // Teaching MVP: only L0→L1 is implemented. L1+ left for leveled follow-up.
    (void)level;
    return Status::Ok();
}

}  // namespace core
}  // namespace minikv
