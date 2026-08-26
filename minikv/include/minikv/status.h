#pragma once
#include <string>

namespace minikv {

enum class StatusCode {
    kOk = 0,
    kNotFound = 1,
    kCorruption = 2,
    kNotSupported = 3,
    kInvalidArgument = 4,
    kIOError = 5,
    kBusy = 6,
};

class Status {
public:
    Status() : code_(StatusCode::kOk) {}
    Status(StatusCode code, std::string msg) : code_(code), msg_(std::move(msg)) {}

    bool ok() const { return code_ == StatusCode::kOk; }
    bool isNotFound() const { return code_ == StatusCode::kNotFound; }
    bool isCorruption() const { return code_ == StatusCode::kCorruption; }
    bool isIOError() const { return code_ == StatusCode::kIOError; }
    bool isBusy() const { return code_ == StatusCode::kBusy; }

    StatusCode code() const { return code_; }
    const std::string& message() const { return msg_; }

    static Status Ok() { return Status(); }
    static Status NotFound(std::string msg = "") { return Status(StatusCode::kNotFound, std::move(msg)); }
    static Status Corruption(std::string msg = "") { return Status(StatusCode::kCorruption, std::move(msg)); }
    static Status NotSupported(std::string msg = "") { return Status(StatusCode::kNotSupported, std::move(msg)); }
    static Status InvalidArgument(std::string msg = "") { return Status(StatusCode::kInvalidArgument, std::move(msg)); }
    static Status IOError(std::string msg = "") { return Status(StatusCode::kIOError, std::move(msg)); }
    static Status Busy(std::string msg = "") { return Status(StatusCode::kBusy, std::move(msg)); }

    std::string ToString() const;

private:
    StatusCode code_;
    std::string msg_;
};

}  // namespace minikv
