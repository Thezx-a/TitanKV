#include "utils/metrics.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include <atomic>
#include <cstring>
#include <sstream>
#include <string>
#include <thread>

namespace minikv {
namespace utils {

namespace {

std::atomic<bool> g_metrics_running{false};
std::thread g_metrics_thread;
int g_listen_fd = -1;

void handleClient(int cfd) {
    char buf[1024];
    ssize_t n = ::recv(cfd, buf, sizeof(buf) - 1, 0);
    if (n <= 0) {
        ::close(cfd);
        return;
    }
    buf[n] = '\0';
    std::string req(buf);
    std::string body;
    std::string content_type = "text/plain; charset=utf-8";
    int code = 200;
    if (req.find("GET /metrics") == 0) {
        body = EngineMetrics::instance().prometheusText();
    } else if (req.find("GET /healthz") == 0) {
        body = "ok\n";
    } else {
        code = 404;
        body = "not found\n";
    }
    std::ostringstream oss;
    oss << "HTTP/1.1 " << code << (code == 200 ? " OK" : " Not Found") << "\r\n"
        << "Content-Type: " << content_type << "\r\n"
        << "Content-Length: " << body.size() << "\r\n"
        << "Connection: close\r\n\r\n"
        << body;
    std::string resp = oss.str();
    ::send(cfd, resp.data(), resp.size(), 0);
    ::close(cfd);
}

void metricsLoop(std::string host, int port) {
    g_listen_fd = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (g_listen_fd < 0) return;
    int yes = 1;
    ::setsockopt(g_listen_fd, SOL_SOCKET, SO_REUSEADDR, &yes, sizeof(yes));
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    if (::inet_pton(AF_INET, host.c_str(), &addr.sin_addr) != 1) {
        addr.sin_addr.s_addr = INADDR_ANY;
    }
    if (::bind(g_listen_fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        ::close(g_listen_fd);
        g_listen_fd = -1;
        return;
    }
    if (::listen(g_listen_fd, 16) < 0) {
        ::close(g_listen_fd);
        g_listen_fd = -1;
        return;
    }
    while (g_metrics_running.load()) {
        int cfd = ::accept(g_listen_fd, nullptr, nullptr);
        if (cfd < 0) {
            if (!g_metrics_running.load()) break;
            continue;
        }
        handleClient(cfd);
    }
    if (g_listen_fd >= 0) {
        ::close(g_listen_fd);
        g_listen_fd = -1;
    }
}

}  // namespace

std::string EngineMetrics::prometheusText() const {
    std::ostringstream oss;
    auto line = [&](const char* name, uint64_t v) {
        oss << name << " " << v << "\n";
    };
    line("titankv_engine_puts_total", puts.load());
    line("titankv_engine_gets_total", gets.load());
    line("titankv_engine_get_hits_total", get_hits.load());
    line("titankv_engine_get_misses_total", get_misses.load());
    line("titankv_engine_deletes_total", deletes.load());
    line("titankv_engine_flushes_total", flushes.load());
    line("titankv_engine_compactions_total", compactions.load());
    line("titankv_engine_compaction_failures_total", compaction_failures.load());
    line("titankv_engine_write_stalls_total", write_stalls.load());
    line("titankv_engine_table_cache_hits_total", table_cache_hits.load());
    line("titankv_engine_table_cache_misses_total", table_cache_misses.load());
    line("titankv_engine_block_cache_hits_total", block_cache_hits.load());
    line("titankv_engine_block_cache_misses_total", block_cache_misses.load());
    return oss.str();
}

bool startMetricsHttp(const std::string& host, int port) {
    if (port <= 0) return false;
    if (g_metrics_running.exchange(true)) return true;  // already running
    g_metrics_thread = std::thread(metricsLoop, host, port);
    // Brief settle — bind failure leaves listen fd -1; callers can curl later.
    return true;
}

void stopMetricsHttp() {
    if (!g_metrics_running.exchange(false)) return;
    if (g_listen_fd >= 0) {
        // Wake accept
        ::shutdown(g_listen_fd, SHUT_RDWR);
        ::close(g_listen_fd);
        g_listen_fd = -1;
    }
    if (g_metrics_thread.joinable()) g_metrics_thread.join();
}

}  // namespace utils
}  // namespace minikv
