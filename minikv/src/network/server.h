#pragma once
#include <memory>
#include <string>
#include "network/event_loop.h"
#include "network/event_loop_thread.h"
#include "utils/thread_pool.h"
#include "minikv/db.h"

namespace minikv {
namespace network {

class Server {
public:
    // io_threads: Sub Reactor count. 0 = single-thread (accept + IO on main).
    // biz_threads: business pool. 0 = processRequest on the Sub IO thread.
    Server(const std::string& host, int port, ::minikv::DB* db, int io_threads = 4,
           int biz_threads = 4);
    ~Server();
    void run();
    void stop();
    int port() const { return port_; }
    int ioThreads() const { return io_threads_; }
    int bizThreads() const { return biz_threads_; }

private:
    void handleNewConnection();
    void registerConnection(EventLoop* io, int conn_fd);
    std::string processRequest(const std::string& rawData);

    std::string host_;
    int port_;
    int io_threads_;
    int biz_threads_;
    ::minikv::DB* db_;
    int listen_fd_;
    EventLoop loop_;
    EventLoopThreadPool io_pool_;
    std::unique_ptr<utils::ThreadPool> biz_pool_;
};

}  // namespace network
}  // namespace minikv
