#pragma once
#include <atomic>
#include <memory>
#include <string>
#include <thread>
#include <vector>
#include "core/block_cache.h"
#include "core/sstable_reader.h"
#include "core/version.h"
#include "minikv/status.h"

namespace minikv {
namespace core {

class CompactionManager {
public:
    CompactionManager(Version* version, const std::string& db_path,
                      size_t block_size = 4096, int max_level = 7,
                      size_t l0_trigger = 4, BlockCache* block_cache = nullptr);
    ~CompactionManager();

    void start();
    void stop();
    void triggerCompaction();

private:
    void compactionLoop();
    Status compactL0();
    Status compactLevel(int level);
    Status mergeLevelFiles(int src_level, const std::vector<std::string>& src_files);
    void invalidateCache(const std::vector<std::string>& paths);

    Version* version_;
    std::string db_path_;
    size_t block_size_;
    int max_level_;
    size_t l0_trigger_;
    BlockCache* block_cache_;
    std::thread compact_thread_;
    std::atomic<bool> running_;
    std::atomic<bool> triggered_;
    int compaction_failures_ = 0;
};

}  // namespace core
}  // namespace minikv
