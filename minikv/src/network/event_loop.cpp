#include "network/event_loop.h"

#include <cerrno>
#include <iostream>
#include <sys/eventfd.h>
#include <unistd.h>

namespace minikv {
namespace network {

EventLoop::EventLoop() : epoll_fd_(-1), wakeup_fd_(-1), thread_id_(std::this_thread::get_id()) {
    epoll_fd_ = ::epoll_create1(EPOLL_CLOEXEC);
    if (epoll_fd_ < 0) {
        std::cerr << "epoll_create1 failed" << std::endl;
        return;
    }
    wakeup_fd_ = ::eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    if (wakeup_fd_ < 0) {
        std::cerr << "eventfd failed" << std::endl;
        return;
    }
    addEvent(wakeup_fd_, EPOLLIN, [this](uint32_t) { handleWakeup(); });
}

EventLoop::~EventLoop() {
    if (wakeup_fd_ >= 0) ::close(wakeup_fd_);
    if (epoll_fd_ >= 0) ::close(epoll_fd_);
}

void EventLoop::addEvent(int fd, uint32_t events, Callback callback) {
    struct epoll_event ev;
    ev.events = events;
    ev.data.fd = fd;
    ::epoll_ctl(epoll_fd_, EPOLL_CTL_ADD, fd, &ev);
    callbacks_[fd] = std::move(callback);
}

void EventLoop::updateEvent(int fd, uint32_t events) {
    struct epoll_event ev;
    ev.events = events;
    ev.data.fd = fd;
    ::epoll_ctl(epoll_fd_, EPOLL_CTL_MOD, fd, &ev);
}

void EventLoop::removeEvent(int fd) {
    ::epoll_ctl(epoll_fd_, EPOLL_CTL_DEL, fd, nullptr);
    callbacks_.erase(fd);
}

void EventLoop::runInLoop(Functor cb) {
    if (isInLoopThread()) {
        cb();
        return;
    }
    queueInLoop(std::move(cb));
}

void EventLoop::queueInLoop(Functor cb) {
    {
        std::lock_guard<std::mutex> lock(pending_mu_);
        pending_.push_back(std::move(cb));
    }
    wakeup();
}

void EventLoop::wakeup() {
    if (wakeup_fd_ < 0) return;
    uint64_t one = 1;
    ssize_t n = ::write(wakeup_fd_, &one, sizeof(one));
    (void)n;
}

void EventLoop::handleWakeup() {
    uint64_t val = 0;
    while (true) {
        ssize_t n = ::read(wakeup_fd_, &val, sizeof(val));
        if (n < 0) {
            if (errno == EAGAIN || errno == EWOULDBLOCK) break;
            if (errno == EINTR) continue;
            break;
        }
        if (n == 0) break;
    }
}

void EventLoop::doPendingFunctors() {
    std::vector<Functor> functors;
    {
        std::lock_guard<std::mutex> lock(pending_mu_);
        functors.swap(pending_);
    }
    for (auto& f : functors) {
        f();
    }
}

void EventLoop::loop() {
    thread_id_ = std::this_thread::get_id();
    running_ = true;
    struct epoll_event events[kMaxEvents];
    while (running_) {
        int n = ::epoll_wait(epoll_fd_, events, kMaxEvents, 200);
        for (int i = 0; i < n; ++i) {
            int fd = events[i].data.fd;
            auto it = callbacks_.find(fd);
            if (it == callbacks_.end()) continue;
            // Copy before invoke: handlers may removeEvent(fd) which would
            // destroy the std::function currently executing (UAF/SIGSEGV).
            Callback cb = it->second;
            cb(events[i].events);
        }
        doPendingFunctors();
    }
}

void EventLoop::stop() {
    running_ = false;
    wakeup();
}

}  // namespace network
}  // namespace minikv
