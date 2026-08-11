#include <iostream>
#include <csignal>
#include <memory>
#include <string>
#include <cstring>
#include <vector>
#include <algorithm>
#include <unistd.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/select.h>
#include "skynet/proxy/upstream.h"
#include "skynet/proxy/load_balancer.h"
#include "skynet/proxy/health_check.h"
#include "skynet/proxy/connection_pool.h"
#include "skynet/http/parser.h"
#include "skynet/http/response.h"
#include "skynet/config/config.h"

static volatile bool g_running = true;
void signalHandler(int) { g_running = false; }

namespace {

void setSocketTimeout(int fd, int timeout_ms) {
    struct timeval tv{
        timeout_ms / 1000,
        (timeout_ms % 1000) * 1000};
    ::setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    ::setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
}

// Read until HTTP request headers+body are complete (or timeout/error).
bool readFullHttpMessage(int fd, std::string* out) {
    skynet::http::HttpParser parser;
    char buf[8192];
    while (true) {
        ssize_t n = ::read(fd, buf, sizeof(buf));
        if (n <= 0) return false;
        out->append(buf, static_cast<size_t>(n));
        auto result = parser.feed(buf, static_cast<size_t>(n));
        if (result == skynet::http::ParseResult::kOk) return true;
        if (result == skynet::http::ParseResult::kError) return false;
        // kNeedMoreData → continue
    }
}

// Parse Content-Length from raw HTTP message; -1 if missing/chunked/unknown.
int parseContentLength(const std::string& msg) {
    auto header_end = msg.find("\r\n\r\n");
    if (header_end == std::string::npos) return -1;
    std::string headers = msg.substr(0, header_end);
    // case-insensitive search for Content-Length
    std::string lower = headers;
    std::transform(lower.begin(), lower.end(), lower.begin(),
                   [](unsigned char c) { return static_cast<char>(::tolower(c)); });
    auto pos = lower.find("content-length:");
    if (pos == std::string::npos) return -1;
    pos += std::strlen("content-length:");
    while (pos < lower.size() && (lower[pos] == ' ' || lower[pos] == '\t')) ++pos;
    try {
        return std::stoi(headers.substr(pos));
    } catch (...) {
        return -1;
    }
}

bool readFullHttpResponse(skynet::net::Socket* sock, std::string* out) {
    char buf[8192];
    while (true) {
        ssize_t n = sock->read(buf, sizeof(buf));
        if (n < 0) return !out->empty();  // timeout/partial
        if (n == 0) return !out->empty(); // peer closed
        out->append(buf, static_cast<size_t>(n));

        auto header_end = out->find("\r\n\r\n");
        if (header_end == std::string::npos) continue;

        int cl = parseContentLength(*out);
        size_t body_start = header_end + 4;
        if (cl >= 0) {
            if (out->size() >= body_start + static_cast<size_t>(cl)) return true;
            continue;
        }
        // No Content-Length: for MVP treat "got headers" as enough for small
        // responses if Connection: close, otherwise keep reading until peer closes.
        std::string head = out->substr(0, header_end);
        std::string lower = head;
        std::transform(lower.begin(), lower.end(), lower.begin(),
                       [](unsigned char c) { return static_cast<char>(::tolower(c)); });
        if (lower.find("transfer-encoding: chunked") != std::string::npos) {
            // crude: wait until trailing \r\n0\r\n\r\n
            if (out->find("\r\n0\r\n\r\n") != std::string::npos) return true;
            continue;
        }
        // Keep reading until peer closes (Gin often keeps-alive with CL though).
        // If we already have headers and a tiny body hint via CL missing, one more
        // non-blocking style attempt is handled by socket timeout returning n<0.
    }
}

void writeAll(int fd, const char* data, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = ::write(fd, data + off, len - off);
        if (n <= 0) return;
        off += static_cast<size_t>(n);
    }
}

}  // namespace

void handle_client(int client_fd, skynet::proxy::LoadBalancer* lb,
                   skynet::proxy::ConnectionPool* pool) {
    setSocketTimeout(client_fd, 5000);

    std::string rawReq;
    if (!readFullHttpMessage(client_fd, &rawReq)) {
        ::close(client_fd);
        return;
    }

    auto upstream = lb->select();
    if (!upstream) {
        const char* resp =
            "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
        writeAll(client_fd, resp, std::strlen(resp));
        ::close(client_fd);
        return;
    }

    upstream->active_connections++;
    auto backendSock = pool->acquire(*upstream);
    if (!backendSock) {
        const char* resp =
            "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
        writeAll(client_fd, resp, std::strlen(resp));
        upstream->active_connections--;
        ::close(client_fd);
        return;
    }
    setSocketTimeout(backendSock->fd(), 5000);

    // Force close so pooled sockets don't need perfect keep-alive framing.
    // Inject Connection: close if missing (simple append before body).
    std::string fwd = rawReq;
    auto he = fwd.find("\r\n\r\n");
    if (he != std::string::npos) {
        std::string head = fwd.substr(0, he);
        std::string lower = head;
        std::transform(lower.begin(), lower.end(), lower.begin(),
                       [](unsigned char c) { return static_cast<char>(::tolower(c)); });
        if (lower.find("connection:") == std::string::npos) {
            fwd.insert(he, "\r\nConnection: close");
        }
    }

    if (backendSock->write(fwd.data(), fwd.size()) < 0) {
        pool->release(std::move(backendSock), *upstream);
        upstream->active_connections--;
        ::close(client_fd);
        return;
    }

    std::string rawResp;
    if (!readFullHttpResponse(backendSock.get(), &rawResp) || rawResp.empty()) {
        const char* resp =
            "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
        writeAll(client_fd, resp, std::strlen(resp));
    } else {
        writeAll(client_fd, rawResp.data(), rawResp.size());
    }

    // Do not reuse: we asked Connection: close; drop socket.
    backendSock->close();
    backendSock.reset();
    upstream->active_connections--;
    ::close(client_fd);
}

int main(int argc, char* argv[]) {
    std::string configPath = "gateway.yaml";
    for (int i = 1; i < argc; ++i) {
        if (std::string(argv[i]) == "--config" && i + 1 < argc) configPath = argv[++i];
    }
    auto cfg = skynet::config::Config::load(configPath);
    if (!cfg) {
        std::cerr << "Failed to load config: " << configPath << std::endl;
        return 1;
    }

    ::signal(SIGINT, signalHandler);
    ::signal(SIGTERM, signalHandler);

    std::vector<skynet::proxy::Upstream> upstreams;
    for (const auto& u : cfg->upstreams) {
        skynet::proxy::Upstream up;
        up.host = u.host;
        up.port = u.port;
        up.weight = u.weight;
        upstreams.push_back(up);
    }

    skynet::proxy::UpstreamManager upstreamMgr;
    upstreamMgr.reload(upstreams);
    skynet::proxy::WeightedRoundRobinLB lb(&upstreamMgr);
    skynet::proxy::ConnectionPool pool(10);
    skynet::proxy::HealthCheck hc(&upstreamMgr, cfg->health_check.interval_s,
                                   cfg->health_check.timeout_ms);
    hc.start();

    int listen_fd = ::socket(AF_INET, SOCK_STREAM, 0);
    int opt = 1;
    ::setsockopt(listen_fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
    struct sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(cfg->listen_port);
    addr.sin_addr.s_addr = INADDR_ANY;
    if (::bind(listen_fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::cerr << "Bind failed on port " << cfg->listen_port << std::endl;
        return 1;
    }
    ::listen(listen_fd, 128);
    std::cout << "SkyNet gateway (front proxy) listening on port " << cfg->listen_port
              << " → Gin upstream(s)" << std::endl;

    // Non-blocking accept via short select so SIGINT can stop the loop.
    while (g_running) {
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(listen_fd, &rfds);
        struct timeval tv{0, 200000};  // 200ms
        int ready = ::select(listen_fd + 1, &rfds, nullptr, nullptr, &tv);
        if (ready <= 0) continue;
        int client_fd = ::accept(listen_fd, nullptr, nullptr);
        if (client_fd < 0) continue;
        handle_client(client_fd, &lb, &pool);
    }
    hc.stop();
    ::close(listen_fd);
    return 0;
}
