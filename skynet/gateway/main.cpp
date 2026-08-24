// skynet_gateway — main/sub Reactor + epoll ET + C++20 coroutines (Client → Gin)
//
// 架构：
//   main reactor (主线程)：1 个 IOContext，只跑 acceptLoop 协程 accept
//   sub  reactor (N 工作线程)：每个一个 IOContext + 自己的 epoll_fd
//     - 主线程 accept 出 client_fd 后 round-robin 投递到某个 sub 的 pending 队列
//     - sub worker drain 队列后 spawn handleClient 协程，在自己线程上 co_await IO
//     - 协程挂起让出 CPU（非阻塞），sub 线程继续 poll 处理其他 fd
//   accept 与 IO 分离，能利用多核；LoadBalancer 跨 sub 共享。
#include <algorithm>
#include <atomic>
#include <chrono>
#include <csignal>
#include <cstring>
#include <iostream>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include "skynet/config/config.h"
#include "skynet/core/detached_task.h"
#include "skynet/http/parser.h"
#include "skynet/net/acceptor.h"
#include "skynet/net/io_awaitable.h"
#include "skynet/net/io_context.h"
#include "skynet/proxy/health_check.h"
#include "skynet/proxy/load_balancer.h"
#include "skynet/proxy/upstream.h"

namespace {

constexpr size_t kMaxRequestBytes = 1 << 20;
constexpr int kShutdownDrainMs = 5000;

std::atomic<bool> g_running{true};
std::atomic<int> g_inflight{0};
std::atomic<int> g_accepted{0};

void signalHandler(int) { g_running = false; }

// SubReactor：每个工作线程一个独立 IOContext + 自己的 epoll_fd，
// 主线程通过 pending 队列把已 accept 的 client 投递过来。
struct SubReactor {
    skynet::net::IOContext ctx;
    std::mutex mq;
    std::vector<std::unique_ptr<skynet::net::Socket>> pending;
    std::atomic<bool> running{true};
    std::thread worker;
};

std::string toLowerCopy(std::string s) {
    std::transform(s.begin(), s.end(), s.begin(),
                   [](unsigned char c) { return static_cast<char>(::tolower(c)); });
    return s;
}

int parseContentLength(const std::string& headers) {
    std::string lower = toLowerCopy(headers);
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

bool headerHas(const std::string& headers_lower, const char* needle) {
    return headers_lower.find(needle) != std::string::npos;
}

// 所有原 g_ctx 引用改为显式传入的 IOContext*（即 SubReactor::ctx）。
skynet::core::Task<bool> sendSimple(skynet::net::Socket& sock, const char* resp,
                                    skynet::net::IOContext* ctx) {
    co_return co_await skynet::net::asyncWriteAll(sock, ctx, resp, std::strlen(resp));
}

skynet::core::Task<bool> readFullHttpRequest(skynet::net::Socket& sock, std::string* out,
                                              skynet::net::IOContext* ctx) {
    skynet::http::HttpParser parser;
    char buf[8192];
    while (true) {
        if (out->size() >= kMaxRequestBytes) co_return false;
        ssize_t n = co_await skynet::net::asyncRead(sock, ctx, buf, sizeof(buf));
        if (n <= 0) co_return false;
        out->append(buf, static_cast<size_t>(n));
        auto result = parser.feed(buf, static_cast<size_t>(n));
        if (result == skynet::http::ParseResult::kOk) co_return true;
        if (result == skynet::http::ParseResult::kError) co_return false;
    }
}

skynet::core::Task<bool> relayResponse(skynet::net::Socket& backend, skynet::net::Socket& client,
                                       skynet::net::IOContext* ctx) {
    std::string buf;
    char tmp[8192];
    bool headers_done = false;
    int content_length = -1;
    bool chunked = false;
    bool event_stream = false;
    size_t body_start = 0;
    size_t already_sent = 0;

    auto flush_new = [&](size_t from) -> skynet::core::Task<bool> {
        if (buf.size() <= from) co_return true;
        co_return co_await skynet::net::asyncWriteAll(client, ctx, buf.data() + from,
                                                      buf.size() - from);
    };

    while (true) {
        if (!g_running.load()) {
            co_return co_await flush_new(already_sent);
        }

        ssize_t n = co_await skynet::net::asyncRead(backend, ctx, tmp, sizeof(tmp));
        if (n <= 0) {
            if (headers_done && content_length >= 0 &&
                buf.size() >= body_start + static_cast<size_t>(content_length)) {
                co_return true;
            }
            if (headers_done && chunked && buf.find("\r\n0\r\n\r\n") != std::string::npos) {
                co_return true;
            }
            co_return already_sent > 0 || !buf.empty();
        }

        buf.append(tmp, static_cast<size_t>(n));

        if (!headers_done) {
            auto he = buf.find("\r\n\r\n");
            if (he == std::string::npos) {
                if (buf.size() > 64 * 1024) co_return false;
                continue;
            }
            headers_done = true;
            body_start = he + 4;
            std::string head = buf.substr(0, he);
            std::string lower = toLowerCopy(head);
            content_length = parseContentLength(head);
            chunked = headerHas(lower, "transfer-encoding: chunked");
            event_stream = headerHas(lower, "content-type: text/event-stream");

            if (!co_await flush_new(already_sent)) co_return false;
            already_sent = buf.size();

            if (content_length >= 0 &&
                buf.size() >= body_start + static_cast<size_t>(content_length)) {
                co_return true;
            }
            if (chunked && buf.find("\r\n0\r\n\r\n") != std::string::npos) co_return true;
            (void)event_stream;
            continue;
        }

        if (!co_await flush_new(already_sent)) co_return false;
        already_sent = buf.size();

        if (content_length >= 0 &&
            buf.size() >= body_start + static_cast<size_t>(content_length)) {
            co_return true;
        }
        if (chunked && buf.find("\r\n0\r\n\r\n") != std::string::npos) co_return true;

        if (event_stream && buf.size() > 256 * 1024) {
            buf.erase(0, buf.size() - 64);
            already_sent = buf.size();
            body_start = 0;
        }
    }
}

// 在 sub reactor 上跑：ctx 用 sr->ctx（sub 自己的 epoll_fd），
// 跨 sub 共享同一个 LoadBalancer（select 内部用 atomic 计数，并发可接受）。
skynet::core::DetachedTask handleClient(std::unique_ptr<skynet::net::Socket> client_sock,
                                        skynet::proxy::LoadBalancer* lb,
                                        SubReactor* sr) {
    struct InflightGuard {
        ~InflightGuard() { g_inflight.fetch_sub(1); }
    } guard;
    g_inflight.fetch_add(1);

    auto* ctx = &sr->ctx;
    auto& client = *client_sock;

    std::string rawReq;
    if (!co_await readFullHttpRequest(client, &rawReq, ctx)) {
        co_await sendSimple(client,
                            "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: "
                            "close\r\n\r\n", ctx);
        client.close();
        co_return;
    }

    auto upstream = lb->select();
    if (!upstream) {
        co_await sendSimple(client,
                            "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: "
                            "close\r\n\r\n", ctx);
        client.close();
        co_return;
    }

    upstream->active_connections++;
    auto backendSock = co_await skynet::net::asyncConnect(ctx, upstream->host, upstream->port);
    if (!backendSock) {
        co_await sendSimple(client,
                            "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: "
                            "close\r\n\r\n", ctx);
        upstream->active_connections--;
        client.close();
        co_return;
    }

    std::string fwd = rawReq;
    auto he = fwd.find("\r\n\r\n");
    if (he != std::string::npos) {
        std::string head = fwd.substr(0, he);
        if (toLowerCopy(head).find("connection:") == std::string::npos) {
            fwd.insert(he, "\r\nConnection: close");
        }
    }

    if (!co_await skynet::net::asyncWriteAll(*backendSock, ctx, fwd.data(), fwd.size())) {
        co_await sendSimple(client,
                            "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: "
                            "close\r\n\r\n", ctx);
        backendSock->close();
        upstream->active_connections--;
        client.close();
        co_return;
    }

    co_await relayResponse(*backendSock, client, ctx);

    backendSock->close();
    upstream->active_connections--;
    client.close();
}

// sub reactor worker 线程入口：drain pending → spawn handleClient → poll 事件循环
void subReactorLoop(SubReactor* sr, skynet::proxy::LoadBalancer* lb, int max_connections) {
    while (sr->running.load() || g_inflight.load() > 0) {
        // 同线程 drain pending（同线程 epoll_ctl_add 安全，避免跨线程竞态）
        std::vector<std::unique_ptr<skynet::net::Socket>> incoming;
        {
            std::lock_guard<std::mutex> lk(sr->mq);
            incoming.swap(sr->pending);
        }
        for (auto& sock : incoming) {
            if (g_inflight.load() >= max_connections) {
                // 超限直接关闭（sock 析构会 close fd）
                sock->close();
                continue;
            }
            g_accepted.fetch_add(1);
            handleClient(std::move(sock), lb, sr);
        }
        sr->ctx.poll(50);
    }
}

// main reactor 上跑：只 accept，fd round-robin 投递到 sub
skynet::core::DetachedTask acceptLoop(skynet::net::Acceptor* acceptor,
                                      skynet::net::IOContext* main_ctx,
                                      std::vector<SubReactor>* subs) {
    size_t idx = 0;
    while (g_running.load()) {
        auto client = co_await acceptor->accept(main_ctx);
        if (!client) continue;
        auto& sr = (*subs)[idx % subs->size()];
        idx++;
        {
            std::lock_guard<std::mutex> lk(sr.mq);
            sr.pending.push_back(std::move(client));
        }
    }
}

}  // namespace

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
    skynet::proxy::HealthCheck hc(&upstreamMgr, cfg->health_check.interval_s,
                                   cfg->health_check.timeout_ms);
    hc.start();

    // 启动 N 个 sub reactor 工作线程（gateway.yaml listen.threads）
    int n_threads = cfg->worker_threads > 0 ? cfg->worker_threads : 1;
    std::vector<SubReactor> subs(static_cast<size_t>(n_threads));
    for (auto& sr : subs) {
        sr.worker = std::thread(subReactorLoop, &sr, &lb, cfg->limits.max_connections);
    }

    // main reactor：仅 accept，把 fd 投递给 sub
    skynet::net::IOContext main_ctx;
    skynet::net::Acceptor acceptor("0.0.0.0", cfg->listen_port);
    if (!acceptor.bindAndListen()) {
        for (auto& sr : subs) { sr.running = false; sr.worker.join(); }
        return 1;
    }

    std::cout << "SkyNet gateway (main/sub Reactor, " << n_threads
              << " sub reactor(s), epoll ET + coroutines) on :" << cfg->listen_port
              << " → Gin upstream(s)" << std::endl;

    acceptLoop(&acceptor, &main_ctx, &subs);

    while (g_running.load()) {
        main_ctx.poll(100);
    }

    std::cout << "Shutting down, drain inflight (max " << kShutdownDrainMs << "ms)..." << std::endl;
    // 1) 通知 sub reactor 退出循环（但等 inflight 归零）
    for (auto& sr : subs) sr.running = false;

    auto deadline = std::chrono::steady_clock::now() +
                    std::chrono::milliseconds(kShutdownDrainMs);
    while (g_inflight.load() > 0 && std::chrono::steady_clock::now() < deadline) {
        // sub reactor 仍 poll 一段时间让挂起协程完成
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    for (auto& sr : subs) {
        if (sr.worker.joinable()) sr.worker.join();
    }

    hc.stop();
    std::cout << "SkyNet gateway stopped. accepted=" << g_accepted.load() << std::endl;
    return 0;
}
