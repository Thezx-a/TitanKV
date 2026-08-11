#pragma once
#include <atomic>
#include <functional>
#include <mutex>
#include <sys/epoll.h>
#include <thread>
#include <unordered_map>
#include <vector>

namespace minikv {
namespace network {

class EventLoop {
public:
    using Callback = std::function<void(uint32_t)>;
    using Functor = std::function<void()>;
    static const int kMaxEvents = 1024;

    EventLoop();
    ~EventLoop();

    EventLoop(const EventLoop&) = delete;
    EventLoop& operator=(const EventLoop&) = delete;

    void addEvent(int fd, uint32_t events, Callback callback);
    void updateEvent(int fd, uint32_t events);
    void removeEvent(int fd);
    void loop();
    void stop();

    void runInLoop(Functor cb);
    void queueInLoop(Functor cb);
    bool isInLoopThread() const { return thread_id_ == std::this_thread::get_id(); }
    std::thread::id threadId() const { return thread_id_; }

private:
    void wakeup();
    void handleWakeup();
    void doPendingFunctors();

    int epoll_fd_;
    int wakeup_fd_;
    std::atomic<bool> running_{false};
    std::thread::id thread_id_;
    std::unordered_map<int, Callback> callbacks_;
    std::mutex pending_mu_;
    std::vector<Functor> pending_;
};

}  // namespace network
}  // namespace minikv
