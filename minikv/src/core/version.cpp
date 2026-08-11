#include "core/version.h"

#include <algorithm>

#include "core/manifest.h"

namespace minikv {
namespace core {

Version::Version() : next_file_number_(1) {
    levels_.resize(7);
}

Version::~Version() = default;

void Version::restoreFrom(const std::vector<std::vector<SSTableMeta>>& snapshot) {
    std::lock_guard<std::mutex> lock(mutex_);
    levels_ = snapshot;
    if (levels_.size() < 7) levels_.resize(7);
    // Bump next_file_number past the largest observed file number.
    uint64_t max_no = 0;
    for (const auto& lvl : levels_) {
        for (const auto& meta : lvl) {
            if (meta.file_number > max_no) max_no = meta.file_number;
        }
    }
    uint64_t cur = next_file_number_.load();
    if (max_no >= cur) next_file_number_.store(max_no + 1);
}

std::vector<std::string> Version::getLevelFiles(int level) const {
    std::lock_guard<std::mutex> lock(mutex_);
    std::vector<std::string> result;
    if (level < 0 || level >= static_cast<int>(levels_.size())) return result;
    for (const auto& meta : levels_[level]) result.push_back(meta.path);
    return result;
}

std::vector<SSTableMeta> Version::getLevelMetas(int level) const {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level < 0 || level >= static_cast<int>(levels_.size())) return {};
    return levels_[level];
}

void Version::addLevelFile(int level, const std::string& path) {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level < 0) return;
    if (level >= static_cast<int>(levels_.size())) levels_.resize(level + 1);

    // Prefer file number encoded in basename ("N.sst") so callers that already
    // reserved a number via nextFileNumber() do not allocate a second id.
    uint64_t file_no = 0;
    bool from_path = false;
    auto slash = path.find_last_of('/');
    std::string base = (slash == std::string::npos) ? path : path.substr(slash + 1);
    auto dot = base.find('.');
    if (dot != std::string::npos && dot > 0) {
        bool all_digits = true;
        for (size_t i = 0; i < dot; ++i) {
            if (base[i] < '0' || base[i] > '9') { all_digits = false; break; }
        }
        if (all_digits) {
            file_no = 0;
            for (size_t i = 0; i < dot; ++i) file_no = file_no * 10 + (base[i] - '0');
            from_path = true;
            uint64_t cur = next_file_number_.load();
            while (file_no + 1 > cur) {
                if (next_file_number_.compare_exchange_weak(cur, file_no + 1)) break;
            }
        }
    }
    if (!from_path) file_no = next_file_number_++;

    levels_[level].push_back({path, "", "", file_no, 0});
    if (manifest_) {
        (void)manifest_->recordAddFile(level, path, file_no);
        (void)manifest_->sync();
    }
}

void Version::removeLevelFiles(int level, const std::vector<std::string>& paths) {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level < 0 || level >= static_cast<int>(levels_.size())) return;
    auto& files = levels_[level];
    files.erase(std::remove_if(files.begin(), files.end(),
        [&](const SSTableMeta& m) {
            bool removed = std::find(paths.begin(), paths.end(), m.path) != paths.end();
            if (removed && manifest_) {
                (void)manifest_->recordRemoveFile(level, m.path, m.file_number);
            }
            return removed;
        }), files.end());
    if (manifest_) (void)manifest_->sync();
}

bool Version::shouldCompactL0() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return !levels_.empty() && levels_[0].size() >= 4;
}

size_t Version::levelSize(int level) const {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level < 0 || level >= static_cast<int>(levels_.size())) return 0;
    return levels_[level].size();
}

uint64_t Version::nextFileNumber() {
    return next_file_number_.fetch_add(1);
}

void Version::ensureNextFileNumberAtLeast(uint64_t min_next) {
    uint64_t cur = next_file_number_.load();
    while (cur < min_next) {
        if (next_file_number_.compare_exchange_weak(cur, min_next)) break;
    }
}

}  // namespace core
}  // namespace minikv