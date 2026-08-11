#include "network/event_loop_thread.h"

namespace minikv {
namespace network {

EventLoopThread::EventLoopThread()
    : loop_(nullptr), exiting_(false) {}

EventLoopThread::~EventLoopThread() { stop(); }

EventLoop* EventLoopThread::startLoop() {
    thread_ = std::thread([this] { threadFunc(); });
    std::unique_lock<std::mutex> lock(mu_);
    cv_.wait(lock, [this] { return loop_ != nullptr; });
    return loop_;
}

void EventLoopThread::stop() {
    EventLoop* loop = nullptr;
    {
        std::lock_guard<std::mutex> lock(mu_);
        if (exiting_) return;
        exiting_ = true;
        loop = loop_;
    }
    if (loop) loop->stop();
    if (thread_.joinable()) thread_.join();
}

void EventLoopThread::threadFunc() {
    EventLoop loop;
    {
        std::lock_guard<std::mutex> lock(mu_);
        loop_ = &loop;
        thread_id_ = std::this_thread::get_id();
        cv_.notify_one();
    }
    loop.loop();
    std::lock_guard<std::mutex> lock(mu_);
    loop_ = nullptr;
}

EventLoopThreadPool::EventLoopThreadPool(EventLoop* main_loop)
    : main_loop_(main_loop), next_(0), started_(false) {}

EventLoopThreadPool::~EventLoopThreadPool() { stop(); }

void EventLoopThreadPool::start(size_t num_threads) {
    if (started_) return;
    started_ = true;
    threads_.reserve(num_threads);
    loops_.reserve(num_threads);
    for (size_t i = 0; i < num_threads; ++i) {
        auto t = std::make_unique<EventLoopThread>();
        loops_.push_back(t->startLoop());
        threads_.push_back(std::move(t));
    }
}

void EventLoopThreadPool::stop() {
    for (auto& t : threads_) {
        if (t) t->stop();
    }
    threads_.clear();
    loops_.clear();
    started_ = false;
    next_ = 0;
}

EventLoop* EventLoopThreadPool::next() {
    if (loops_.empty()) return main_loop_;
    EventLoop* loop = loops_[next_ % loops_.size()];
    ++next_;
    return loop;
}

}  // namespace network
}  // namespace minikv
