#pragma once
#include <memory>
#include <string>
#include "minikv/status.h"

namespace minikv {
namespace utils {

class WritableFile {
public:
    virtual ~WritableFile() = default;
    virtual Status append(const std::string& data) = 0;
    virtual Status sync() = 0;
    virtual Status close() = 0;
};

class Env {
public:
    virtual ~Env() = default;
    virtual Status newWritableFile(const std::string& path, std::unique_ptr<WritableFile>* result) = 0;
    virtual bool fileExists(const std::string& path) = 0;
    virtual Status deleteFile(const std::string& path) = 0;
    virtual Status createDir(const std::string& path) = 0;
};

Env* defaultEnv();

// fsync the directory containing `path` (or `path` itself if it is a dir).
// Needed after rename/create so directory entries survive power loss.
// Honest: WSL cannot truly simulate power loss; this enforces correct order.
Status fsyncDir(const std::string& path);

}  // namespace utils
}  // namespace minikv
