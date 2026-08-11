#pragma once
#include <cstdint>
#include <string>
#include <vector>
#include "minikv/slice.h"
#include "minikv/status.h"
#include "minikv/write_batch.h"

namespace minikv {
namespace core {

class WAL {
public:
    explicit WAL(const std::string& path);
    ~WAL();

    Status append(const Slice& data);
    Status sync();
    std::vector<std::string> replay();
    // Low-level reset of this file (tests). DB flush path prefers a new WAL file.
    Status truncate();

    bool exists() const;
    const std::string& path() const { return path_; }

private:
    std::string path_;
    int fd_;
};

}  // namespace core
}  // namespace minikv
