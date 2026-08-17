#include "skynet/proxy/connection_pool.h"
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <unistd.h>

namespace skynet {
namespace proxy {

namespace {

constexpr int kConnectTimeoutMs = 2000;

bool connectWithTimeout(int fd, const sockaddr* addr, socklen_t len, int timeout_ms) {
    int flags = ::fcntl(fd, F_GETFL, 0);
    if (flags < 0) return false;
    if (::fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0) return false;

    int rc = ::connect(fd, addr, len);
    if (rc == 0) {
        ::fcntl(fd, F_SETFL, flags);
        return true;
    }
    if (errno != EINPROGRESS) {
        return false;
    }

    fd_set wfds;
    FD_ZERO(&wfds);
    FD_SET(fd, &wfds);
    struct timeval tv{timeout_ms / 1000, (timeout_ms % 1000) * 1000};
    rc = ::select(fd + 1, nullptr, &wfds, nullptr, &tv);
    if (rc <= 0) return false;

    int so_error = 0;
    socklen_t errlen = sizeof(so_error);
    if (::getsockopt(fd, SOL_SOCKET, SO_ERROR, &so_error, &errlen) < 0 || so_error != 0) {
        return false;
    }
    ::fcntl(fd, F_SETFL, flags);
    return true;
}

}  // namespace

ConnectionPool::ConnectionPool(size_t max_idle) : max_idle_(max_idle) {}

std::unique_ptr<net::Socket> ConnectionPool::acquire(const Upstream& up) {
    {
        std::lock_guard<std::mutex> lock(mutex_);
        std::string key = up.host + ":" + std::to_string(up.port);
        auto it = pools_.find(key);
        if (it != pools_.end() && !it->second.idle.empty()) {
            auto sock = std::move(it->second.idle.front());
            it->second.idle.pop();
            return sock;
        }
    }
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return nullptr;
    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(up.port));
    if (::inet_pton(AF_INET, up.host.c_str(), &addr.sin_addr) != 1) {
        ::close(fd);
        return nullptr;
    }
    if (!connectWithTimeout(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr),
                            kConnectTimeoutMs)) {
        ::close(fd);
        return nullptr;
    }
    return std::make_unique<net::Socket>(fd);
}

void ConnectionPool::release(std::unique_ptr<net::Socket> sock, const Upstream& up) {
    std::lock_guard<std::mutex> lock(mutex_);
    std::string key = up.host + ":" + std::to_string(up.port);
    auto& pool = pools_[key];
    if (pool.idle.size() < max_idle_) {
        pool.idle.push(std::move(sock));
    }
}

}  // namespace proxy
}  // namespace skynet
