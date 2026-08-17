#pragma once

#include <arpa/inet.h>
#include <cerrno>
#include <cstring>
#include <memory>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include "skynet/core/task.h"
#include "skynet/net/io_context.h"
#include "skynet/net/socket.h"

namespace skynet {
namespace net {

// Suspend until fd is readable/writable (epoll ET one-shot via IOContext::watchOnce).
class IoAwaitable {
public:
    IoAwaitable(int fd, uint32_t events, IOContext* ctx)
        : fd_(fd), events_(events), ctx_(ctx) {}

    bool await_ready() const noexcept { return false; }

    void await_suspend(std::coroutine_handle<> h) {
        handle_ = h;
        ctx_->watchOnce(fd_, events_, [this](uint32_t) { handle_.resume(); });
    }

    void await_resume() noexcept {}

private:
    int fd_;
    uint32_t events_;
    IOContext* ctx_;
    std::coroutine_handle<> handle_;
};

inline core::Task<ssize_t> asyncRead(Socket& sock, IOContext* ctx, void* buf, size_t len) {
    while (true) {
        ssize_t n = sock.read(buf, len);
        if (n > 0) co_return n;
        if (n == 0) co_return 0;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            co_await IoAwaitable(sock.fd(), EPOLLIN, ctx);
            continue;
        }
        co_return -1;
    }
}

inline core::Task<bool> asyncWriteAll(Socket& sock, IOContext* ctx, const char* data,
                                      size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = sock.write(data + off, len - off);
        if (n > 0) {
            off += static_cast<size_t>(n);
            continue;
        }
        if (n == 0) co_return false;
        if (errno == EINTR) continue;
        if (errno == EAGAIN || errno == EWOULDBLOCK) {
            co_await IoAwaitable(sock.fd(), EPOLLOUT, ctx);
            continue;
        }
        co_return false;
    }
    co_return true;
}

inline core::Task<std::unique_ptr<Socket>> asyncConnect(IOContext* ctx, const std::string& host,
                                                        int port) {
    int fd = ::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK, 0);
    if (fd < 0) co_return nullptr;

    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    if (::inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
        ::close(fd);
        co_return nullptr;
    }

    int rc = ::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr));
    if (rc == 0) {
        co_return std::make_unique<Socket>(fd);
    }
    if (errno != EINPROGRESS) {
        ::close(fd);
        co_return nullptr;
    }

    co_await IoAwaitable(fd, EPOLLOUT, ctx);

    int so_error = 0;
    socklen_t errlen = sizeof(so_error);
    if (::getsockopt(fd, SOL_SOCKET, SO_ERROR, &so_error, &errlen) < 0 || so_error != 0) {
        ::close(fd);
        co_return nullptr;
    }
    co_return std::make_unique<Socket>(fd);
}

}  // namespace net
}  // namespace skynet
