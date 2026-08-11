#pragma once
#include <atomic>
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

    Connection(int fd, RequestHandler handler, EventLoop* loop = nullptr,
               utils::ThreadPool* pool = nullptr);
    ~Connection();

    void onReadable();
    void onWritable();
    void markClosed();
    int fd() const { return fd_; }
    bool shouldClose() const { return close_.load(); }

private:
    void tryDispatch();

    int fd_;
    RequestHandler handler_;
    EventLoop* loop_;
    utils::ThreadPool* pool_;
    std::string read_buf_;
    std::string write_buf_;
    std::atomic<bool> close_{false};
    bool in_flight_ = false;
};

}  // namespace network
}  // namespace minikv
