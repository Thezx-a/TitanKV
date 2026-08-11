// Module 13 题15 + 题 S3：手撕线程池
// 要求：mutex + condition_variable + atomic<bool> stop
//       submit 模板方法（返回 std::future）
// 测试：提交 1000 任务，验证全部完成
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <atomic>
#include <condition_variable>
#include <functional>
#include <future>
#include <memory>
#include <mutex>
#include <queue>
#include <thread>
#include <vector>

namespace {

class ThreadPool {
public:
    explicit ThreadPool(size_t threads) : stop_(false) {
        workers_.reserve(threads);
        for (size_t i = 0; i < threads; ++i) {
            workers_.emplace_back([this] {
                for (;;) {
                    std::function<void()> task;
                    {
                        std::unique_lock<std::mutex> lock(mtx_);
                        cv_.wait(lock, [this] { return stop_.load() || !tasks_.empty(); });
                        if (stop_.load() && tasks_.empty()) return;
                        task = std::move(tasks_.front());
                        tasks_.pop();
                    }
                    task();
                }
            });
        }
    }

    ~ThreadPool() {
        stop_.store(true);
        cv_.notify_all();
        for (auto& w : workers_) {
            if (w.joinable()) w.join();
        }
    }

    ThreadPool(const ThreadPool&) = delete;
    ThreadPool& operator=(const ThreadPool&) = delete;

    // 提交任务，返回 future 以获取结果。
    template <typename F, typename... Args>
    auto submit(F&& f, Args&&... args)
        -> std::future<typename std::invoke_result<F, Args...>::type> {
        using R = typename std::invoke_result<F, Args...>::type;
        auto task = std::make_shared<std::packaged_task<R()>>(
            std::bind(std::forward<F>(f), std::forward<Args>(args)...));
        std::future<R> fut = task->get_future();
        {
            std::unique_lock<std::mutex> lock(mtx_);
            tasks_.emplace([task]() { (*task)(); });
        }
        cv_.notify_one();
        return fut;
    }

private:
    std::vector<std::thread> workers_;
    std::queue<std::function<void()>> tasks_;
    std::mutex mtx_;
    std::condition_variable cv_;
    std::atomic<bool> stop_;
};

}  // namespace

// ===== 单元测试 =====

TEST(ThreadPoolHandwriteTest, SubmitSingleTaskReturnsValue) {
    ThreadPool pool(2);
    auto fut = pool.submit([](int a, int b) { return a + b; }, 3, 4);
    EXPECT_EQ(fut.get(), 7);
}

TEST(ThreadPoolHandwriteTest, SubmitThousandTasksAllComplete) {
    ThreadPool pool(4);
    constexpr int N = 1000;
    std::vector<std::future<int>> futs;
    futs.reserve(N);
    std::atomic<int> counter{0};
    for (int i = 0; i < N; ++i) {
        futs.push_back(pool.submit([&counter, i] {
            counter.fetch_add(1);
            return i * 2;
        }));
    }
    int sum = 0;
    for (auto& f : futs) sum += f.get();
    EXPECT_EQ(counter.load(), N);
    // 0+2+...+1998 = 2*(0+1+...+999) = 2 * 999*1000/2 = 999000
    EXPECT_EQ(sum, 999000);
}

TEST(ThreadPoolHandwriteTest, ConcurrentMapIncrementStress) {
    // 1000 个任务对 1000 个不同下标加 1，验证无竞争错乱（用 future 同步）。
    ThreadPool pool(8);
    constexpr int N = 1000;
    std::vector<int> data(N, 0);
    std::vector<std::future<void>> futs;
    futs.reserve(N);
    for (int i = 0; i < N; ++i) {
        futs.push_back(pool.submit([&data, i] { data[i] += 1; }));
    }
    for (auto& f : futs) f.get();
    for (int i = 0; i < N; ++i) EXPECT_EQ(data[i], 1);
}

TEST(ThreadPoolHandwriteTest, ExceptionPropagatesViaFuture) {
    ThreadPool pool(1);
    auto fut = pool.submit([]() -> int {
        throw std::runtime_error("boom");
    });
    EXPECT_THROW(fut.get(), std::runtime_error);
}
