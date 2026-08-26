#include "network/connection.h"
#include <cerrno>
#include <cstring>
#include <unistd.h>
#include <sys/epoll.h>
#include "network/event_loop.h"
#include "network/protocol.h"
#include "utils/thread_pool.h"

namespace minikv {
namespace network {

Connection::Connection(int fd, RequestHandler handler, EventLoop* loop,
                       utils::ThreadPool* pool)
    : fd_(fd),
      handler_(std::move(handler)),
      loop_(loop),
      pool_(pool) {}

Connection::~Connection() {
    if (fd_ >= 0) {
        ::close(fd_);
        fd_ = -1;
    }
}

void Connection::markClosed() { close_ = true; }

bool Connection::checkRequestBounds(size_t total_size) {
    if (total_size < sizeof(RequestHeader) || total_size > kMaxRequestSize) {
        close_ = true;
        return false;
    }
    return true;
}

void Connection::enableWriteInterest() {
    if (!loop_ || writing_ || close_) return;
    writing_ = true;
    loop_->updateEvent(fd_, EPOLLIN | EPOLLOUT);
}

void Connection::disableWriteInterest() {
    if (!loop_ || !writing_) return;
    writing_ = false;
    loop_->updateEvent(fd_, EPOLLIN);
}

void Connection::onReadable() {
    char buf[4096];
    ssize_t n = ::read(fd_, buf, sizeof(buf));
    if (n < 0) {
        if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) return;
        close_ = true;
        return;
    }
    if (n == 0) {
        close_ = true;
        return;
    }
    read_buf_.append(buf, n);
    if (read_buf_.size() > kMaxRequestSize) {
        close_ = true;
        return;
    }

    if (pool_ && loop_) {
        tryDispatch();
        return;
    }

    while (read_buf_.size() >= sizeof(RequestHeader)) {
        auto* hdr = reinterpret_cast<const RequestHeader*>(read_buf_.data());
        if (hdr->magic != kProtocolMagic) {
            close_ = true;
            return;
        }
        size_t totalSize = sizeof(RequestHeader) +
                           static_cast<size_t>(hdr->key_len) +
                           static_cast<size_t>(hdr->val_len);
        if (!checkRequestBounds(totalSize)) return;
        if (read_buf_.size() < totalSize) break;

        std::string rawData = read_buf_.substr(0, totalSize);
        read_buf_.erase(0, totalSize);
        std::string response = handler_(rawData);
        write_buf_.append(response);
        if (write_buf_.size() > kMaxWriteBuffer) {
            close_ = true;
            return;
        }
    }
    if (!write_buf_.empty()) onWritable();
}

void Connection::tryDispatch() {
    if (close_ || in_flight_ || !pool_ || !loop_) return;
    if (read_buf_.size() < sizeof(RequestHeader)) return;

    auto* hdr = reinterpret_cast<const RequestHeader*>(read_buf_.data());
    if (hdr->magic != kProtocolMagic) {
        close_ = true;
        return;
    }
    size_t totalSize = sizeof(RequestHeader) +
                       static_cast<size_t>(hdr->key_len) +
                       static_cast<size_t>(hdr->val_len);
    if (!checkRequestBounds(totalSize)) return;
    if (read_buf_.size() < totalSize) return;

    std::string rawData = read_buf_.substr(0, totalSize);
    read_buf_.erase(0, totalSize);
    in_flight_ = true;

    auto self = shared_from_this();
    pool_->submit([self, rawData] {
        std::string response;
        if (!self->close_) {
            response = self->handler_(rawData);
        }
        self->loop_->queueInLoop([self, response = std::move(response)] {
            if (!self->close_) {
                self->write_buf_.append(response);
                if (self->write_buf_.size() > kMaxWriteBuffer) {
                    self->close_ = true;
                } else {
                    self->onWritable();
                }
            }
            self->in_flight_ = false;
            if (!self->close_) self->tryDispatch();
        });
    });
}

void Connection::onWritable() {
    while (!write_buf_.empty()) {
        ssize_t n = ::write(fd_, write_buf_.data(), write_buf_.size());
        if (n > 0) {
            write_buf_.erase(0, static_cast<size_t>(n));
            continue;
        }
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR)) {
            enableWriteInterest();
            return;
        }
        close_ = true;
        return;
    }
    disableWriteInterest();
}

}  // namespace network
}  // namespace minikv
