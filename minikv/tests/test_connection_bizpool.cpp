#include <gtest/gtest.h>

#include <fcntl.h>
#include <sys/socket.h>
#include <unistd.h>

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <mutex>
#include <string>
#include <thread>

#include "network/connection.h"
#include "network/event_loop.h"
#include "network/protocol.h"
#include "utils/thread_pool.h"

using minikv::network::Cmd;
using minikv::network::Connection;
using minikv::network::EventLoop;
using minikv::network::encodeRequest;
using minikv::network::encodeResponse;
using minikv::network::ResponseStatus;
using minikv::utils::ThreadPool;

namespace {

void setNonblock(int fd) {
    int flags = ::fcntl(fd, F_GETFL, 0);
    ::fcntl(fd, F_SETFL, flags | O_NONBLOCK);
}

}  // namespace

// Slow handler must run off the IO thread so EventLoop can still drain
// queueInLoop (eventfd wakeup) while business is blocked.
TEST(ConnectionBizPoolTest, SlowHandlerDoesNotBlockEventLoop) {
    int fds[2];
    ASSERT_EQ(::socketpair(AF_UNIX, SOCK_STREAM, 0, fds), 0);
    setNonblock(fds[0]);
    setNonblock(fds[1]);

    EventLoop loop;
    ThreadPool pool(2);

    std::mutex mu;
    std::condition_variable cv;
    bool handler_entered = false;
    bool handler_release = false;

    auto conn = std::make_shared<Connection>(
        fds[0],
        [&](const std::string&) {
            {
                std::lock_guard<std::mutex> lk(mu);
                handler_entered = true;
            }
            cv.notify_all();
            std::unique_lock<std::mutex> lk(mu);
            cv.wait(lk, [&] { return handler_release; });
            return encodeResponse(ResponseStatus::kOk, minikv::Slice());
        },
        &loop, &pool);

    loop.addEvent(fds[0], EPOLLIN, [conn](uint32_t) { conn->onReadable(); });

    std::thread io([&] { loop.loop(); });

    std::string req = encodeRequest(Cmd::kPut, "k", "v");
    ASSERT_EQ(::write(fds[1], req.data(), req.size()),
              static_cast<ssize_t>(req.size()));

    {
        std::unique_lock<std::mutex> lk(mu);
        ASSERT_TRUE(cv.wait_for(lk, std::chrono::seconds(2),
                                [&] { return handler_entered; }));
    }

    std::atomic<bool> io_ran{false};
    loop.queueInLoop([&] { io_ran = true; });

    const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(500);
    while (!io_ran.load() && std::chrono::steady_clock::now() < deadline) {
        std::this_thread::sleep_for(std::chrono::milliseconds(10));
    }
    EXPECT_TRUE(io_ran.load()) << "EventLoop stuck in handler_; business must be in the pool";

    {
        std::lock_guard<std::mutex> lk(mu);
        handler_release = true;
    }
    cv.notify_all();

    loop.stop();
    io.join();
    pool.stop();
    loop.removeEvent(fds[0]);
    ::close(fds[1]);
}
