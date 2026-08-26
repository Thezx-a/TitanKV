#include <gtest/gtest.h>

#include <atomic>
#include <chrono>
#include <cstdlib>
#include <string>
#include <thread>

#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

#include "minikv/db.h"
#include "minikv/options.h"
#include "utils/metrics.h"

using minikv::DB;
using minikv::Options;
using minikv::ReadOptions;
using minikv::WriteOptions;
using minikv::utils::EngineMetrics;
using minikv::utils::startMetricsHttp;
using minikv::utils::stopMetricsHttp;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    return std::string(t) + "/titankv_metrics_" + std::to_string(::getpid()) +
           "_" + std::to_string(counter.fetch_add(1));
}

void rmTree(const std::string& root) {
    int rc = std::system(("rm -rf " + root).c_str());
    (void)rc;
}

std::string httpGet(const std::string& host, int port, const std::string& path) {
    int fd = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) return {};
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_port = htons(static_cast<uint16_t>(port));
    ::inet_pton(AF_INET, host.c_str(), &addr.sin_addr);
    if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) != 0) {
        ::close(fd);
        return {};
    }
    std::string req = "GET " + path + " HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n";
    ::send(fd, req.data(), req.size(), 0);
    std::string resp;
    char buf[4096];
    for (;;) {
        ssize_t n = ::recv(fd, buf, sizeof(buf), 0);
        if (n <= 0) break;
        resp.append(buf, static_cast<size_t>(n));
    }
    ::close(fd);
    auto pos = resp.find("\r\n\r\n");
    if (pos == std::string::npos) return resp;
    return resp.substr(pos + 4);
}

}  // namespace

TEST(EngineMetricsTest, PrometheusTextContainsPrefixes) {
    auto& m = EngineMetrics::instance();
    m.puts.fetch_add(1);
    std::string text = m.prometheusText();
    EXPECT_NE(text.find("titankv_engine_puts_total"), std::string::npos);
    EXPECT_NE(text.find("titankv_engine_gets_total"), std::string::npos);
    EXPECT_NE(text.find("titankv_engine_table_cache_hits_total"), std::string::npos);
}

TEST(EngineMetricsTest, HttpMetricsAndHealthz) {
    const int port = 19000 + static_cast<int>(::getpid() % 1000);
    ASSERT_TRUE(startMetricsHttp("127.0.0.1", port));
    // Wait briefly for bind/listen.
    std::string body;
    for (int i = 0; i < 50; ++i) {
        body = httpGet("127.0.0.1", port, "/healthz");
        if (body.find("ok") != std::string::npos) break;
        std::this_thread::sleep_for(std::chrono::milliseconds(20));
    }
    EXPECT_NE(body.find("ok"), std::string::npos);

    body = httpGet("127.0.0.1", port, "/metrics");
    EXPECT_NE(body.find("titankv_engine_"), std::string::npos);

    stopMetricsHttp();
}

TEST(EngineMetricsTest, PutGetBumpCounters) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.wal_sync = false;

    auto& m = EngineMetrics::instance();
    const uint64_t puts0 = m.puts.load();
    const uint64_t gets0 = m.gets.load();

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());
    WriteOptions wo;
    wo.sync = false;
    ASSERT_TRUE(db->put(wo, "mk", "mv").ok());
    ReadOptions ro;
    std::string got;
    ASSERT_TRUE(db->get(ro, "mk", &got).ok());
    EXPECT_EQ(got, "mv");

    EXPECT_GE(m.puts.load(), puts0 + 1);
    EXPECT_GE(m.gets.load(), gets0 + 1);
    EXPECT_GE(m.get_hits.load(), 1u);

    rmTree(dir);
}
