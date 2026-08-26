#include <iostream>
#include <cstdlib>
#include <memory>
#include <string>
#include <csignal>
#include <atomic>
#include "core/db_impl.h"
#include "network/server.h"
#include "minikv/options.h"
#include "utils/log.h"
#include "utils/metrics.h"

static std::atomic<bool> g_running{true};
static ::minikv::network::Server* g_server = nullptr;

void signalHandler(int) {
    g_running = false;
    if (g_server) g_server->stop();
}

int main(int argc, char* argv[]) {
    int port = 8888;
    int ioThreads = 4;
    int bizThreads = 4;
    int metricsPort = 0;
    std::string host = "0.0.0.0";
    std::string dbPath = "./minikv_data";

    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        if (arg == "--port" && i + 1 < argc) port = std::atoi(argv[++i]);
        else if (arg == "--host" && i + 1 < argc) host = argv[++i];
        else if (arg == "--db" && i + 1 < argc) dbPath = argv[++i];
        else if (arg == "--io-threads" && i + 1 < argc) ioThreads = std::atoi(argv[++i]);
        else if (arg == "--biz-threads" && i + 1 < argc) bizThreads = std::atoi(argv[++i]);
        else if (arg == "--metrics-port" && i + 1 < argc) metricsPort = std::atoi(argv[++i]);
    }

    ::signal(SIGINT, signalHandler);
    ::signal(SIGTERM, signalHandler);

    ::minikv::Options opts;
    opts.db_path = dbPath;
    opts.wal_sync = false;

    std::unique_ptr<::minikv::DB> db;
    auto status = ::minikv::core::DBImpl::open(opts, &db);
    if (!status.ok()) {
        std::cerr << "Failed to open DB: " << status.message() << std::endl;
        return 1;
    }

    LOG_INFO("MiniKV starting on " + host + ":" + std::to_string(port) +
             " io_threads=" + std::to_string(ioThreads) +
             " biz_threads=" + std::to_string(bizThreads));

    if (metricsPort > 0) {
        if (::minikv::utils::startMetricsHttp(host, metricsPort)) {
            LOG_INFO("metrics on " + host + ":" + std::to_string(metricsPort) + " /metrics");
        }
    }
    ::minikv::network::Server server(host, port, db.get(), ioThreads, bizThreads);
    g_server = &server;
    server.run();
    g_server = nullptr;

    ::minikv::utils::stopMetricsHttp();
    LOG_INFO("MiniKV shutting down");
    return g_running ? 0 : 0;
}
