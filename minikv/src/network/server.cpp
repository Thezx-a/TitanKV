#include "network/server.h"
#include <arpa/inet.h>
#include <cerrno>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>
#include <cstring>
#include <iostream>
#include <memory>
#include <string>
#include <vector>
#include "core/internal_key.h"
#include "minikv/write_batch.h"
#include "network/connection.h"
#include "network/protocol.h"
#include "utils/coding.h"

namespace minikv {
namespace network {

namespace {

// Scan payload (little-endian):
//   count:u32
//   repeated: key_len:u32 | key | val_len:u32 | val
std::string encodeScanPayload(
    const std::vector<std::pair<std::string, std::string>>& items) {
    std::string out;
    char buf[4];
    utils::encodeFixed32(buf, static_cast<uint32_t>(items.size()));
    out.append(buf, 4);
    for (const auto& kv : items) {
        utils::encodeFixed32(buf, static_cast<uint32_t>(kv.first.size()));
        out.append(buf, 4);
        out.append(kv.first);
        utils::encodeFixed32(buf, static_cast<uint32_t>(kv.second.size()));
        out.append(buf, 4);
        out.append(kv.second);
    }
    return out;
}

}  // namespace

Server::Server(const std::string& host, int port, ::minikv::DB* db, int io_threads,
               int biz_threads)
    : host_(host),
      port_(port),
      io_threads_(io_threads < 0 ? 0 : io_threads),
      biz_threads_(biz_threads < 0 ? 0 : biz_threads),
      db_(db),
      listen_fd_(-1),
      io_pool_(&loop_) {
    if (biz_threads_ > 0) {
        biz_pool_ = std::make_unique<utils::ThreadPool>(
            static_cast<size_t>(biz_threads_));
    }
    listen_fd_ = ::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (listen_fd_ < 0) {
        std::cerr << "socket() failed" << std::endl;
        return;
    }
    int opt = 1;
    ::setsockopt(listen_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port_));
    ::inet_pton(AF_INET, host.c_str(), &addr.sin_addr);

    if (::bind(listen_fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Bind failed on port " << port_ << std::endl;
        ::close(listen_fd_);
        listen_fd_ = -1;
        return;
    }
    if (::listen(listen_fd_, 128) < 0) {
        std::cerr << "listen() failed" << std::endl;
        ::close(listen_fd_);
        listen_fd_ = -1;
        return;
    }
    if (port_ == 0) {
        socklen_t alen = sizeof(addr);
        if (::getsockname(listen_fd_, reinterpret_cast<sockaddr*>(&addr), &alen) == 0) {
            port_ = ntohs(addr.sin_port);
        }
    }
    std::cout << "MiniKV server listening on " << host_ << ":" << port_
              << " (main reactor + " << io_threads_ << " sub reactor"
              << (io_threads_ == 1 ? "" : "s")
              << ", biz_threads=" << biz_threads_ << ")" << std::endl;
}

Server::~Server() {
    if (listen_fd_ >= 0) ::close(listen_fd_);
}

void Server::run() {
    if (listen_fd_ < 0) {
        std::cerr << "Server not listening; abort run()" << std::endl;
        return;
    }
    io_pool_.start(static_cast<size_t>(io_threads_));
    loop_.addEvent(listen_fd_, EPOLLIN, [this](uint32_t) { handleNewConnection(); });
    loop_.loop();
    if (biz_pool_) biz_pool_->stop();
    io_pool_.stop();
}

void Server::stop() { loop_.stop(); }

void Server::handleNewConnection() {
    while (true) {
        sockaddr_in clientAddr{};
        socklen_t len = sizeof(clientAddr);
        int connFd = ::accept4(listen_fd_, reinterpret_cast<sockaddr*>(&clientAddr),
                               &len, SOCK_NONBLOCK | SOCK_CLOEXEC);
        if (connFd < 0) {
            if (errno == EINTR) continue;
            break;
        }
        EventLoop* io = io_pool_.next();
        io->runInLoop([this, io, connFd] { registerConnection(io, connFd); });
    }
}

void Server::registerConnection(EventLoop* io, int connFd) {
    auto conn = std::make_shared<Connection>(
        connFd,
        [this](const std::string& raw) { return processRequest(raw); },
        io, biz_pool_.get());
    io->addEvent(connFd, EPOLLIN, [io, conn, connFd](uint32_t events) {
        conn->onReadable();
        if (conn->shouldClose() || (events & (EPOLLERR | EPOLLHUP))) {
            conn->markClosed();
            io->removeEvent(connFd);
        }
    });
}

std::string Server::processRequest(const std::string& rawData) {
    if (rawData.size() < sizeof(RequestHeader)) {
        return encodeResponse(ResponseStatus::kError, Slice());
    }
    auto* hdr = reinterpret_cast<const RequestHeader*>(rawData.data());
    if (hdr->magic != kProtocolMagic) {
        return encodeResponse(ResponseStatus::kError, Slice("bad magic", 9));
    }
    const char* key = rawData.data() + sizeof(RequestHeader);
    const char* val = key + hdr->key_len;
    size_t valLen = hdr->val_len;

    auto cmd = static_cast<Cmd>(hdr->cmd);
    WriteOptions wopts;
    ReadOptions ropts;

    switch (cmd) {
        case Cmd::kPut: {
            Status s = db_->put(wopts, Slice(key, hdr->key_len), Slice(val, valLen));
            if (!s.ok()) {
                return encodeResponse(ResponseStatus::kError, Slice(s.message()));
            }
            return encodeResponse(ResponseStatus::kOk, Slice());
        }
        case Cmd::kGet: {
            std::string value;
            Status s = db_->get(ropts, Slice(key, hdr->key_len), &value);
            if (s.ok()) return encodeResponse(ResponseStatus::kOk, value);
            return encodeResponse(ResponseStatus::kNotFound, Slice());
        }
        case Cmd::kDel: {
            Status s = db_->del(wopts, Slice(key, hdr->key_len));
            if (!s.ok()) {
                return encodeResponse(ResponseStatus::kError, Slice(s.message()));
            }
            return encodeResponse(ResponseStatus::kOk, Slice());
        }
        case Cmd::kScan: {
            // key = start (may be empty), value = end (may be empty); range [start, end)
            std::string start(key, hdr->key_len);
            std::string end(val, valLen);

            auto it = db_->newIterator(ropts);
            it->seekToFirst();

            std::vector<std::pair<std::string, std::string>> items;
            std::string last_user;
            while (it->valid()) {
                Slice ik = it->key();
                if (ik.size() < core::kTrailerBytes) {
                    it->next();
                    continue;
                }
                Slice uk = core::InternalKeyUserKey(ik);
                std::string uks = uk.toString();
                if (!start.empty() && uks < start) {
                    it->next();
                    continue;
                }
                if (!end.empty() && uks >= end) break;
                if (uks == last_user) {
                    it->next();
                    continue;
                }
                last_user = uks;
                if (core::InternalKeyType(ik) == core::ValueType::kDeletion) {
                    it->next();
                    continue;
                }
                items.emplace_back(uks, it->value().toString());
                it->next();
            }
            if (!it->status().ok()) {
                return encodeResponse(ResponseStatus::kError, Slice(it->status().message()));
            }
            std::string payload = encodeScanPayload(items);
            return encodeResponse(ResponseStatus::kOk, payload);
        }
        case Cmd::kBatch: {
            // val payload: count:u32 | repeated (op:u8, key_len:u32, key, val_len:u32, val)
            if (valLen < 4) {
                return encodeResponse(ResponseStatus::kError, Slice("bad batch", 9));
            }
            const char* p = val;
            const char* limit = val + valLen;
            uint32_t count = utils::decodeFixed32(p);
            p += 4;
            WriteBatch batch;
            for (uint32_t i = 0; i < count; ++i) {
                if (p + 1 + 4 > limit) {
                    return encodeResponse(ResponseStatus::kError, Slice("truncated batch", 15));
                }
                uint8_t op = static_cast<uint8_t>(*p++);
                uint32_t klen = utils::decodeFixed32(p);
                p += 4;
                if (p + klen + 4 > limit) {
                    return encodeResponse(ResponseStatus::kError, Slice("truncated key", 13));
                }
                std::string k(p, klen);
                p += klen;
                uint32_t vlen = utils::decodeFixed32(p);
                p += 4;
                if (p + vlen > limit) {
                    return encodeResponse(ResponseStatus::kError, Slice("truncated val", 13));
                }
                std::string v(p, vlen);
                p += vlen;
                if (op == static_cast<uint8_t>(BatchOpType::kPut)) {
                    batch.put(Slice(k), Slice(v));
                } else if (op == static_cast<uint8_t>(BatchOpType::kDelete)) {
                    batch.del(Slice(k));
                } else {
                    return encodeResponse(ResponseStatus::kError, Slice("bad batch op", 12));
                }
            }
            Status s = db_->write(wopts, batch);
            if (!s.ok()) {
                return encodeResponse(ResponseStatus::kError, Slice(s.message()));
            }
            return encodeResponse(ResponseStatus::kOk, Slice());
        }
        case Cmd::kDeleteRange: {
            std::string start(key, hdr->key_len);
            std::string end(val, valLen);
            auto it = db_->newIterator(ropts);
            it->seekToFirst();
            WriteBatch batch;
            std::string last_user;
            while (it->valid()) {
                Slice ik = it->key();
                if (ik.size() < core::kTrailerBytes) {
                    it->next();
                    continue;
                }
                Slice uk = core::InternalKeyUserKey(ik);
                std::string uks = uk.toString();
                if (!start.empty() && uks < start) {
                    it->next();
                    continue;
                }
                if (!end.empty() && uks >= end) break;
                if (uks == last_user) {
                    it->next();
                    continue;
                }
                last_user = uks;
                batch.del(Slice(uks));
                it->next();
            }
            if (batch.count() == 0) {
                return encodeResponse(ResponseStatus::kOk, Slice());
            }
            Status s = db_->write(wopts, batch);
            if (!s.ok()) {
                return encodeResponse(ResponseStatus::kError, Slice(s.message()));
            }
            return encodeResponse(ResponseStatus::kOk, Slice());
        }
        default:
            return encodeResponse(ResponseStatus::kError, Slice("Unknown command", 15));
    }
}

}  // namespace network
}  // namespace minikv
