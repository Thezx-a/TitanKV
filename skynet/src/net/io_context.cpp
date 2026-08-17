#include "skynet/net/io_context.h"
#include <unistd.h>

namespace skynet {
namespace net {

IOContext::IOContext() {
    epoll_fd_ = ::epoll_create1(0);
}

IOContext::~IOContext() {
    if (epoll_fd_ >= 0) ::close(epoll_fd_);
}

void IOContext::add(int fd, uint32_t events, Callback cb) {
    watchOnce(fd, events, std::move(cb));
}

void IOContext::watchOnce(int fd, uint32_t events, Callback cb) {
    struct epoll_event ev{};
    ev.events = events | EPOLLET;
    ev.data.fd = fd;
    if (callbacks_.find(fd) != callbacks_.end()) {
        ::epoll_ctl(epoll_fd_, EPOLL_CTL_MOD, fd, &ev);
    } else {
        ::epoll_ctl(epoll_fd_, EPOLL_CTL_ADD, fd, &ev);
    }
    callbacks_[fd] = std::move(cb);
}

void IOContext::modify(int fd, uint32_t events) {
    struct epoll_event ev;
    ev.events = events | EPOLLET;
    ev.data.fd = fd;
    ::epoll_ctl(epoll_fd_, EPOLL_CTL_MOD, fd, &ev);
}

void IOContext::remove(int fd) {
    ::epoll_ctl(epoll_fd_, EPOLL_CTL_DEL, fd, nullptr);
    callbacks_.erase(fd);
}

bool IOContext::poll(int timeout_ms) {
    struct epoll_event events[kMaxEvents];
    int n = ::epoll_wait(epoll_fd_, events, kMaxEvents, timeout_ms);
    for (int i = 0; i < n; ++i) {
        int fd = events[i].data.fd;
        auto it = callbacks_.find(fd);
        if (it == callbacks_.end()) continue;
        Callback cb = std::move(it->second);
        callbacks_.erase(it);
        ::epoll_ctl(epoll_fd_, EPOLL_CTL_DEL, fd, nullptr);
        cb(events[i].events);
    }
    return n > 0;
}

}  // namespace net
}  // namespace skynet
