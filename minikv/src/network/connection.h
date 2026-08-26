#pragma once
#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include "minikv/status.h"

namespace minikv {
namespace utils {
class ThreadPool;
}
namespace network {

class EventLoop;

class Connection : public std::enable_shared_from_this<Connection> {
public:
    using RequestHandler = std::function<std::string(const std::string&)>;

    // Hard caps (M8). Oversize read/write → close connection (no unbounded buffers).
    static constexpr size_t kMaxRequestSize = 64u << 20;   // 64 MiB
    static constexpr size_t kMaxWriteBuffer = 256u << 20;  // 256 MiB

    Connection(int fd, RequestHandler handler, EventLoop* loop = nullptr,
               utils::ThreadPool* pool = nullptr);
    ~Connection();

    void onReadable();
    void onWritable();
    void markClosed();
    int fd() const { return fd_; }
    bool shouldClose() const { return close_.load(); }
    size_t readBufSize() const { return read_buf_.size(); }
    size_t writeBufSize() const { return write_buf_.size(); }

private:
    void tryDispatch();
    bool checkRequestBounds(size_t total_size);
    void enableWriteInterest();
    void disableWriteInterest();

    int fd_;
    RequestHandler handler_;
    EventLoop* loop_;
    utils::ThreadPool* pool_;
    std::string read_buf_;
    std::string write_buf_;
    std::atomic<bool> close_{false};
    bool in_flight_ = false;
    bool writing_ = false;
};

}  // namespace network
}  // namespace minikv
