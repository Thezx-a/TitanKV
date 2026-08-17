#include "skynet/net/acceptor.h"
#include "skynet/net/io_context.h"
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>
#include <fcntl.h>
#include <iostream>

namespace skynet {
namespace net {

AcceptAwaitable::AcceptAwaitable(int listen_fd, IOContext* ctx)
    : listen_fd_(listen_fd), ctx_(ctx), client_fd_(-1) {}

bool AcceptAwaitable::await_ready() const {
    client_fd_ = ::accept4(listen_fd_, nullptr, nullptr, SOCK_NONBLOCK);
    return client_fd_ >= 0;
}

void AcceptAwaitable::await_suspend(std::coroutine_handle<> h) {
    handle_ = h;
    ctx_->watchOnce(listen_fd_, EPOLLIN, [this](uint32_t) {
        client_fd_ = ::accept4(listen_fd_, nullptr, nullptr, SOCK_NONBLOCK);
        handle_.resume();
    });
}

std::unique_ptr<Socket> AcceptAwaitable::await_resume() {
    if (client_fd_ < 0) return nullptr;
    return std::make_unique<Socket>(client_fd_);
}

Acceptor::Acceptor(const std::string& host, int port)
    : host_(host), port_(port), listen_fd_(-1) {}

Acceptor::~Acceptor() {
    if (listen_fd_ >= 0) ::close(listen_fd_);
}

bool Acceptor::bindAndListen() {
    listen_fd_ = ::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK, 0);
    if (listen_fd_ < 0) return false;
    int opt = 1;
    ::setsockopt(listen_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(port_);
    ::inet_pton(AF_INET, host_.c_str(), &addr.sin_addr);

    if (::bind(listen_fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Bind failed on " << host_ << ":" << port_ << std::endl;
        return false;
    }
    ::listen(listen_fd_, 128);
    std::cout << "SkyNet listening on " << host_ << ":" << port_ << std::endl;
    return true;
}

AcceptAwaitable Acceptor::accept(IOContext* ctx) {
    return AcceptAwaitable(listen_fd_, ctx);
}

}  // namespace net
}  // namespace skynet
