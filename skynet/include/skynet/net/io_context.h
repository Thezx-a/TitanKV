#pragma once
#include <cstdint>
#include <functional>
#include <sys/epoll.h>
#include <unordered_map>

namespace skynet {
namespace net {

class IOContext {
public:
    using Callback = std::function<void(uint32_t)>;
    static const int kMaxEvents = 1024;

    IOContext();
    ~IOContext();
    void add(int fd, uint32_t events, Callback cb);
    // One-shot watch for coroutines: fires once, then removes fd from epoll (ET-safe re-arm).
    void watchOnce(int fd, uint32_t events, Callback cb);
    void modify(int fd, uint32_t events);
    void remove(int fd);
    bool poll(int timeout_ms);

private:
    int epoll_fd_;
    std::unordered_map<int, Callback> callbacks_;
};

}  // namespace net
}  // namespace skynet
