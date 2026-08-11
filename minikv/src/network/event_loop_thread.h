#pragma once
#include <condition_variable>
#include <memory>
#include <mutex>
#include <thread>
#include <vector>
#include "network/event_loop.h"

namespace minikv {
namespace network {

// One EventLoop living on its own thread (a Sub Reactor).
class EventLoopThread {
public:
    EventLoopThread();
    ~EventLoopThread();

    EventLoopThread(const EventLoopThread&) = delete;
    EventLoopThread& operator=(const EventLoopThread&) = delete;

    EventLoop* startLoop();
    void stop();
    std::thread::id threadId() const { return thread_id_; }

private:
    void threadFunc();

    EventLoop* loop_;
    std::thread thread_;
    std::thread::id thread_id_;
    std::mutex mu_;
    std::condition_variable cv_;
    bool exiting_;
};

// Round-robin pool of Sub Reactors. num_threads==0 means IO stays on main_loop.
class EventLoopThreadPool {
public:
    explicit EventLoopThreadPool(EventLoop* main_loop);
    ~EventLoopThreadPool();

    EventLoopThreadPool(const EventLoopThreadPool&) = delete;
    EventLoopThreadPool& operator=(const EventLoopThreadPool&) = delete;

    void start(size_t num_threads);
    void stop();
    EventLoop* next();
    size_t numThreads() const { return threads_.size(); }

private:
    EventLoop* main_loop_;
    std::vector<std::unique_ptr<EventLoopThread>> threads_;
    std::vector<EventLoop*> loops_;
    size_t next_;
    bool started_;
};

}  // namespace network
}  // namespace minikv
