#include "minikv/status.h"
#include <sstream>

namespace minikv {

std::string Status::ToString() const {
    std::ostringstream oss;
    switch (code_) {
        case StatusCode::kOk: oss << "OK"; break;
        case StatusCode::kNotFound: oss << "NotFound: "; break;
        case StatusCode::kCorruption: oss << "Corruption: "; break;
        case StatusCode::kNotSupported: oss << "NotSupported: "; break;
        case StatusCode::kInvalidArgument: oss << "InvalidArgument: "; break;
        case StatusCode::kIOError: oss << "IOError: "; break;
        case StatusCode::kBusy: oss << "Busy: "; break;
    }
    if (!msg_.empty()) oss << msg_;
    return oss.str();
}

}  // namespace minikv
