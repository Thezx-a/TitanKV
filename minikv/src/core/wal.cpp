#include "core/wal.h"
#include <fcntl.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#include <cerrno>
#include <cstdio>
#include <cstring>
#include <iostream>
#include "utils/crc32.h"
#include "utils/coding.h"

namespace minikv {
namespace core {

static const uint32_t kWalMagic = 0x4D4B5741;  // "MKWA"

WAL::WAL(const std::string& path) : path_(path), fd_(-1) {
    fd_ = ::open(path_.c_str(), O_RDWR | O_CREAT | O_APPEND, 0644);
}

WAL::~WAL() {
    if (fd_ >= 0) ::close(fd_);
}

Status WAL::append(const Slice& data) {
    if (fd_ < 0) return Status::IOError("WAL not open");
    uint32_t crc = utils::crc32c(data.data(), static_cast<int>(data.size()));
    uint32_t len = static_cast<uint32_t>(data.size());

    char header[8];
    utils::encodeFixed32(header, crc);
    utils::encodeFixed32(header + 4, len);

    if (::write(fd_, header, 8) != 8) return Status::IOError("WAL write header failed");
    if (len > 0 && ::write(fd_, data.data(), len) != static_cast<ssize_t>(len))
        return Status::IOError("WAL write data failed");
    return Status::Ok();
}

Status WAL::sync() {
    if (fd_ >= 0 && ::fdatasync(fd_) != 0)
        return Status::IOError("WAL fsync failed");
    return Status::Ok();
}

std::vector<std::string> WAL::replay() {
    std::vector<std::string> records;
    if (fd_ < 0) return records;
    ::lseek(fd_, 0, SEEK_SET);
    off_t good_offset = 0;
    char header[8];
    while (true) {
        ssize_t n = ::read(fd_, header, 8);
        if (n == 0) break;          // clean EOF
        if (n != 8) break;          // torn header
        uint32_t crc = utils::decodeFixed32(header);
        uint32_t len = utils::decodeFixed32(header + 4);
        // Absurd length ⇒ treat as corruption and stop (do not allocate).
        if (len > (1u << 28)) break;
        std::string data(len, '\0');
        if (len > 0) {
            n = ::read(fd_, data.data(), len);
            if (n != static_cast<ssize_t>(len)) break;  // torn payload
        }
        uint32_t actual = utils::crc32c(data.data(), static_cast<int>(len));
        if (actual != crc) break;
        records.push_back(std::move(data));
        good_offset = ::lseek(fd_, 0, SEEK_CUR);
    }
    // [P0-2] Truncate torn tail so subsequent appends are not stranded past a
    // CRC hole (which would make them invisible on the next reopen).
    off_t end = ::lseek(fd_, 0, SEEK_END);
    if (end > good_offset) {
        std::cerr << "[WARN] truncated " << (end - good_offset)
                  << " bytes of torn WAL tail: " << path_ << std::endl;
        if (::ftruncate(fd_, good_offset) != 0) {
            std::cerr << "[WARN] WAL ftruncate failed errno=" << errno
                      << " path=" << path_ << std::endl;
        }
    }
    ::lseek(fd_, 0, SEEK_END);
    return records;
}

Status WAL::truncate() {
    // After a MemTable flush the log is durable in SST; reset the log file but
    // keep an open append fd so later writes still hit disk.
    if (fd_ >= 0) {
        ::close(fd_);
        fd_ = -1;
    }
    ::unlink(path_.c_str());
    fd_ = ::open(path_.c_str(), O_RDWR | O_CREAT | O_APPEND, 0644);
    if (fd_ < 0) return Status::IOError("WAL reopen after truncate failed");
    return Status::Ok();
}

bool WAL::exists() const {
    struct stat st;
    return ::stat(path_.c_str(), &st) == 0;
}

}  // namespace core
}  // namespace minikv
