#include "core/manifest.h"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <cstring>

#include "utils/coding.h"
#include "utils/crc32.h"

namespace minikv {
namespace core {

static const char* kManifestName = "MANIFEST";

Manifest::Manifest(const std::string& db_path)
    : manifest_path_(db_path + "/" + kManifestName) {}

Manifest::~Manifest() {
    if (fd_ >= 0) ::close(fd_);
}

Status Manifest::open() {
    std::lock_guard<std::mutex> lock(mutex_);
    if (fd_ >= 0) return Status::Ok();
    ::mkdir(/*dirname*/ manifest_path_.substr(0, manifest_path_.find_last_of('/')).c_str(), 0755);
    // Reset in-memory state.
    levels_.assign(8, {});
    recoverActivePathUnlocked();
    // Open for read+append+creat. We need to support both fresh and existing.
    fd_ = ::open(manifest_path_.c_str(), O_RDWR | O_CREAT, 0644);
    if (fd_ < 0) return Status::IOError("cannot open MANIFEST");
    auto s = replay();
    if (!s.ok()) return s;
    return Status::Ok();
}

void Manifest::recoverActivePathUnlocked() {
    // Crash mid-rewriteSnapshot may leave MANIFEST.new and/or MANIFEST.bak.
    // Prefer: existing MANIFEST > promote .new > restore .bak.
    struct stat st;
    if (::stat(manifest_path_.c_str(), &st) == 0 && S_ISREG(st.st_mode)) {
        ::unlink((manifest_path_ + ".new").c_str());  // stale temp
        return;
    }
    std::string neu = manifest_path_ + ".new";
    std::string bak = manifest_path_ + ".bak";
    if (::stat(neu.c_str(), &st) == 0 && S_ISREG(st.st_mode)) {
        (void)::rename(neu.c_str(), manifest_path_.c_str());
        return;
    }
    if (::stat(bak.c_str(), &st) == 0 && S_ISREG(st.st_mode)) {
        (void)::rename(bak.c_str(), manifest_path_.c_str());
    }
}

Status Manifest::replay() {
    ::lseek(fd_, 0, SEEK_SET);
    std::vector<std::vector<SSTableMeta>> tmp;
    tmp.assign(8, {});

    while (true) {
        char header[8];
        ssize_t n = ::read(fd_, header, 8);
        if (n != 8) break;
        uint32_t crc  = utils::decodeFixed32(header);
        uint32_t plen = utils::decodeFixed32(header + 4);
        if (plen == 0 || plen > (1u << 24)) break;
        std::string payload(plen, '\0');
        n = ::read(fd_, payload.data(), plen);
        if (n != static_cast<ssize_t>(plen)) break;
        if (utils::crc32c(payload.data(), static_cast<int>(plen)) != crc) break;

        if (payload.empty()) break;
        RecordType type = static_cast<RecordType>(payload[0]);
        if (type == kReset) {
            tmp.assign(8, {});
            continue;
        }
        if (plen < 1 + 4 + 8 + 4) continue;
        uint32_t level   = utils::decodeFixed32(payload.data() + 1);
        uint64_t file_no = utils::decodeFixed64(payload.data() + 5);
        uint32_t path_len = utils::decodeFixed32(payload.data() + 13);
        if (1 + 4 + 8 + 4 + path_len > plen) break;
        std::string path(payload.data() + 17, path_len);
        if (level >= tmp.size()) tmp.resize(level + 1);
        if (type == kAdd) {
            tmp[level].push_back({path, "", "", file_no, 0});
        } else /* kDel */ {
            auto& vec = tmp[level];
            vec.erase(std::remove_if(vec.begin(), vec.end(),
                [&](const SSTableMeta& m) { return m.path == path; }),
                vec.end());
        }
    }
    levels_ = std::move(tmp);
    ::lseek(fd_, 0, SEEK_END);
    return Status::Ok();
}

Status Manifest::recordReset() {
    std::lock_guard<std::mutex> lock(mutex_);
    char payload[1] = {static_cast<char>(kReset)};
    return writeRecord(kReset, /*level=*/0, /*path=*/"", /*file_no=*/0);
}

Status Manifest::recordAddFile(int level, const std::string& path, uint64_t file_no) {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level >= static_cast<int>(levels_.size())) levels_.resize(level + 1);
    levels_[level].push_back({path, "", "", file_no, 0});
    return writeRecord(kAdd, level, path, file_no);
}

Status Manifest::recordRemoveFile(int level, const std::string& path, uint64_t file_no) {
    std::lock_guard<std::mutex> lock(mutex_);
    if (level < static_cast<int>(levels_.size())) {
        auto& vec = levels_[level];
        vec.erase(std::remove_if(vec.begin(), vec.end(),
            [&](const SSTableMeta& m) { return m.path == path; }),
            vec.end());
    }
    return writeRecord(kDel, level, path, file_no);
}

Status Manifest::sync() {
    std::lock_guard<std::mutex> lock(mutex_);
    if (fd_ >= 0 && ::fdatasync(fd_) != 0)
        return Status::IOError("MANIFEST sync failed");
    return Status::Ok();
}

Status Manifest::writeRecord(RecordType type, int level,
                             const std::string& path, uint64_t file_no) {
    return writeRecordToFd(fd_, type, level, path, file_no);
}

Status Manifest::writeRecordToFd(int fd, RecordType type, int level,
                                 const std::string& path, uint64_t file_no) {
    if (fd < 0) return Status::IOError("MANIFEST fd not open");
    std::string payload;
    payload.push_back(static_cast<char>(static_cast<uint8_t>(type)));
    char buf32[4];
    utils::encodeFixed32(buf32, static_cast<uint32_t>(level));
    payload.append(buf32, 4);
    char buf64[8];
    utils::encodeFixed64(buf64, file_no);
    payload.append(buf64, 8);
    utils::encodeFixed32(buf32, static_cast<uint32_t>(path.size()));
    payload.append(buf32, 4);
    payload.append(path);
    uint32_t crc = utils::crc32c(payload.data(), static_cast<int>(payload.size()));
    char header[8];
    utils::encodeFixed32(header, crc);
    utils::encodeFixed32(header + 4, static_cast<uint32_t>(payload.size()));
    if (::write(fd, header, 8) != 8) return Status::IOError("MANIFEST write header");
    if (::write(fd, payload.data(), payload.size()) != static_cast<ssize_t>(payload.size()))
        return Status::IOError("MANIFEST write payload");
    return Status::Ok();
}

Status Manifest::rewriteSnapshot() {
    std::lock_guard<std::mutex> lock(mutex_);
    // Full live SST set after replay — must be written into the NEW file.
    auto snapshot = levels_;

    std::string neu = manifest_path_ + ".new";
    std::string bak = manifest_path_ + ".bak";

    int nfd = ::open(neu.c_str(), O_RDWR | O_CREAT | O_TRUNC, 0644);
    if (nfd < 0) return Status::IOError("cannot create MANIFEST.new");

    for (size_t level = 0; level < snapshot.size(); ++level) {
        for (const auto& meta : snapshot[level]) {
            Status s = writeRecordToFd(nfd, kAdd, static_cast<int>(level),
                                       meta.path, meta.file_number);
            if (!s.ok()) {
                ::close(nfd);
                ::unlink(neu.c_str());
                return s;
            }
        }
    }
    if (::fdatasync(nfd) != 0) {
        ::close(nfd);
        ::unlink(neu.c_str());
        return Status::IOError("MANIFEST.new sync failed");
    }
    ::close(nfd);

    // Publish: archive old active log, then promote .new.
    if (fd_ >= 0) {
        ::close(fd_);
        fd_ = -1;
    }
    ::unlink(bak.c_str());
    // rename may fail if MANIFEST did not exist yet (fresh DB) — that is OK.
    (void)::rename(manifest_path_.c_str(), bak.c_str());
    if (::rename(neu.c_str(), manifest_path_.c_str()) != 0) {
        // Try to roll back bak -> MANIFEST so next open still works.
        (void)::rename(bak.c_str(), manifest_path_.c_str());
        return Status::IOError("cannot promote MANIFEST.new");
    }

    fd_ = ::open(manifest_path_.c_str(), O_RDWR | O_CREAT, 0644);
    if (fd_ < 0) return Status::IOError("cannot reopen MANIFEST after snapshot");
    ::lseek(fd_, 0, SEEK_END);
    levels_ = std::move(snapshot);
    if (levels_.size() < 8) levels_.resize(8);
    return Status::Ok();
}

size_t Manifest::totalFiles() const {
    size_t s = 0;
    for (const auto& v : levels_) s += v.size();
    return s;
}

}  // namespace core
}  // namespace minikv