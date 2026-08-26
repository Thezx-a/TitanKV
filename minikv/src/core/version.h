#pragma once
#include <atomic>
#include <mutex>
#include <string>
#include <vector>

namespace minikv {
namespace core {

// Forward declaration to avoid a circular dependency with manifest.h.
class Manifest;

struct SSTableMeta {
    std::string path;
    std::string min_key;     // populated lazily; empty until populated.
    std::string max_key;
    uint64_t    file_number;
    uint64_t    file_size;
};

class Version {
public:
    // level_count = max_level + 1 (indices 0..max_level). Default 8.
    explicit Version(int level_count = 8);
    ~Version();

    // Grow capacity if needed (never shrink — preserves SST refs).
    void ensureLevelCapacity(int level_count);

    // Plugs this Version into a Manifest so all mutations are persisted.
    void setManifest(Manifest* m) { manifest_ = m; }
    Manifest* manifest() const { return manifest_; }

    // Restore in-memory state from a previously-loaded Manifest snapshot.
    // Used once during DBImpl::open before WAL replay.
    void restoreFrom(const std::vector<std::vector<SSTableMeta>>& snapshot);

    std::vector<std::string> getLevelFiles(int level) const;
    std::vector<SSTableMeta> getLevelMetas(int level) const;
    void addLevelFile(int level, const std::string& path);
    void removeLevelFiles(int level, const std::vector<std::string>& paths);
    bool shouldCompactL0(size_t trigger = 4) const;
    bool shouldCompactLevel(int level, size_t file_trigger = 2) const;
    size_t levelSize(int level) const;
    uint64_t nextFileNumber();
    // Ensure the next allocated number is >= min_next (does not return a number).
    void ensureNextFileNumberAtLeast(uint64_t min_next);

private:
    mutable std::mutex mutex_;
    std::vector<std::vector<SSTableMeta>> levels_;
    std::atomic<uint64_t> next_file_number_;
    Manifest* manifest_ = nullptr;
};

}  // namespace core
}  // namespace minikv