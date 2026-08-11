#include <gtest/gtest.h>

#include <arpa/inet.h>
#include <cerrno>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include "core/db_impl.h"
#include "minikv/options.h"
#include "network/protocol.h"
#include "network/server.h"

using minikv::Options;
using minikv::core::DBImpl;
using minikv::network::Cmd;
using minikv::network::ResponseHeader;
using minikv::network::ResponseStatus;
using minikv::network::Server;
using minikv::network::encodeRequest;
using minikv::network::kProtocolMagic;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    uint64_t n = counter.fetch_add(1);
    return std::string(t) + "/titankv_server_reactor_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

bool writeAll(int fd, const std::string& data) {
    size_t off = 0;
    while (off < data.size()) {
        ssize_t n = ::write(fd, data.data() + off, data.size() - off);
        if (n < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        off += static_cast<size_t>(n);
    }
    return true;
}

bool readExact(int fd, char* buf, size_t n) {
    size_t off = 0;
    while (off < n) {
        ssize_t r = ::read(fd, buf + off, n - off);
        if (r < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        if (r == 0) return false;
        off += static_cast<size_t>(r);
    }
    return true;
}

int connectPort(int port) {
    int fd = ::socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return -1;
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    ::inet_pton(AF_INET, "127.0.0.1", &addr.sin_addr);
    if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        ::close(fd);
        return -1;
    }
    return fd;
}

bool putGetRoundtrip(int port, const std::string& key, const std::string& val) {
    int fd = connectPort(port);
    if (fd < 0) return false;

    std::string req = encodeRequest(Cmd::kPut, key, val);
    if (!writeAll(fd, req)) {
        ::close(fd);
        return false;
    }
    char rhdr[sizeof(ResponseHeader)];
    if (!readExact(fd, rhdr, sizeof(rhdr))) {
        ::close(fd);
        return false;
    }
    auto* putHdr = reinterpret_cast<ResponseHeader*>(rhdr);
    if (putHdr->magic != kProtocolMagic ||
        putHdr->status != static_cast<uint8_t>(ResponseStatus::kOk)) {
        ::close(fd);
        return false;
    }
    if (putHdr->val_len > 0) {
        std::string skip(putHdr->val_len, '\0');
        if (!readExact(fd, skip.data(), skip.size())) {
            ::close(fd);
            return false;
        }
    }

    std::string greq = encodeRequest(Cmd::kGet, key, minikv::Slice());
    if (!writeAll(fd, greq)) {
        ::close(fd);
        return false;
    }
    if (!readExact(fd, rhdr, sizeof(rhdr))) {
        ::close(fd);
        return false;
    }
    auto* getHdr = reinterpret_cast<ResponseHeader*>(rhdr);
    if (getHdr->magic != kProtocolMagic ||
        getHdr->status != static_cast<uint8_t>(ResponseStatus::kOk) ||
        getHdr->val_len != val.size()) {
        ::close(fd);
        return false;
    }
    std::string got(getHdr->val_len, '\0');
    if (!readExact(fd, got.data(), got.size())) {
        ::close(fd);
        return false;
    }
    ::close(fd);
    return got == val;
}

}  // namespace

TEST(ServerReactorTest, ConcurrentClientsOnTwoSubReactors) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.wal_sync = false;

    std::unique_ptr<minikv::DB> db;
    ASSERT_TRUE(DBImpl::open(opts, &db).ok());

    Server server("127.0.0.1", 0, db.get(), /*io_threads=*/2);
    ASSERT_GT(server.port(), 0);
    EXPECT_EQ(server.ioThreads(), 2);

    std::thread thr([&] { server.run(); });

    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(2);
    int probe = -1;
    while (std::chrono::steady_clock::now() < deadline) {
        probe = connectPort(server.port());
        if (probe >= 0) break;
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    ASSERT_GE(probe, 0) << "server did not accept";
    ::close(probe);

    constexpr int kClients = 8;
    std::vector<std::thread> clients;
    std::atomic<int> ok{0};
    for (int i = 0; i < kClients; ++i) {
        clients.emplace_back([&, i] {
            std::string key = "k" + std::to_string(i);
            std::string val = "v" + std::to_string(i);
            if (putGetRoundtrip(server.port(), key, val)) ok.fetch_add(1);
        });
    }
    for (auto& c : clients) c.join();
    EXPECT_EQ(ok.load(), kClients);

    server.stop();
    thr.join();
    std::string rm = "rm -rf " + dir;
    int rc = std::system(rm.c_str());
    (void)rc;
}
