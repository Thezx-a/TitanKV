#include "utils/env.h"
#include <cerrno>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>
#include <string>

namespace minikv {
namespace utils {

class PosixWritableFile : public WritableFile {
public:
    explicit PosixWritableFile(int fd) : fd_(fd) {}
    ~PosixWritableFile() override { if (fd_ >= 0) ::close(fd_); }

    Status append(const std::string& data) override {
        if (::write(fd_, data.data(), data.size()) != static_cast<ssize_t>(data.size()))
            return Status::IOError("Write failed");
        return Status::Ok();
    }

    Status sync() override {
        if (::fdatasync(fd_) != 0) return Status::IOError("fdatasync failed");
        return Status::Ok();
    }

    Status close() override {
        if (fd_ >= 0) { ::close(fd_); fd_ = -1; }
        return Status::Ok();
    }

private:
    int fd_;
};

class PosixEnv : public Env {
public:
    Status newWritableFile(const std::string& path, std::unique_ptr<WritableFile>* result) override {
        int fd = ::open(path.c_str(), O_WRONLY | O_CREAT | O_TRUNC, 0644);
        if (fd < 0) return Status::IOError("Cannot open: " + path);
        result->reset(new PosixWritableFile(fd));
        return Status::Ok();
    }

    bool fileExists(const std::string& path) override {
        struct stat st;
        return ::stat(path.c_str(), &st) == 0;
    }

    Status deleteFile(const std::string& path) override {
        if (::unlink(path.c_str()) != 0) return Status::IOError("Cannot delete: " + path);
        return Status::Ok();
    }

    Status createDir(const std::string& path) override {
        if (::mkdir(path.c_str(), 0755) != 0 && errno != EEXIST)
            return Status::IOError("Cannot mkdir: " + path);
        return Status::Ok();
    }
};

Env* defaultEnv() {
    static PosixEnv env;
    return &env;
}

Status fsyncDir(const std::string& path) {
    if (path.empty()) return Status::InvalidArgument("empty path for fsyncDir");
    struct stat st;
    std::string dir = path;
    if (::stat(path.c_str(), &st) == 0 && S_ISDIR(st.st_mode)) {
        dir = path;
    } else {
        auto slash = path.find_last_of('/');
        if (slash == std::string::npos) dir = ".";
        else if (slash == 0) dir = "/";
        else dir = path.substr(0, slash);
    }
    int dfd = ::open(dir.c_str(), O_RDONLY | O_DIRECTORY);
    if (dfd < 0) return Status::IOError("cannot open dir for fsync: " + dir);
    int rc = ::fsync(dfd);
    int err = errno;
    ::close(dfd);
    if (rc != 0) {
        return Status::IOError("fsync dir failed: " + dir +
                               " errno=" + std::to_string(err));
    }
    return Status::Ok();
}

}  // namespace utils
}  // namespace minikv
