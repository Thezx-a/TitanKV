#include <gtest/gtest.h>

#include <atomic>
#include <chrono>
#include <thread>
#include <unistd.h>
#include <vector>

#include "network/event_loop.h"
#include "network/event_loop_thread.h"

using minikv::network::EventLoop;
using minikv::network::EventLoopThread;
using minikv::network::EventLoopThreadPool;

TEST(EventLoopTest, RunInLoopFromOtherThreadRunsOnLoopThread) {
    EventLoopThread thr;
    EventLoop* loop = thr.startLoop();
    ASSERT_NE(loop, nullptr);

    std::atomic<bool> done{false};
    std::thread::id ran_on;
    loop->runInLoop([&] {
        ran_on = std::this_thread::get_id();
        done.store(true);
    });

    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(2);
    while (!done.load() && std::chrono::steady_clock::now() < deadline) {
        std::this_thread::sleep_for(std::chrono::milliseconds(5));
    }
    ASSERT_TRUE(done.load()) << "runInLoop did not wake the loop thread";
    EXPECT_EQ(ran_on, thr.threadId());
    EXPECT_NE(ran_on, std::this_thread::get_id());

    thr.stop();
}

TEST(EventLoopThreadPoolTest, RoundRobinReturnsDistinctLoops) {
    EventLoop main_loop;
    EventLoopThreadPool pool(&main_loop);
    pool.start(2);
    ASSERT_EQ(pool.numThreads(), 2u);

    EventLoop* a = pool.next();
    EventLoop* b = pool.next();
    EventLoop* c = pool.next();
    ASSERT_NE(a, nullptr);
    ASSERT_NE(b, nullptr);
    EXPECT_NE(a, b);
    EXPECT_EQ(a, c);

    pool.stop();
}
