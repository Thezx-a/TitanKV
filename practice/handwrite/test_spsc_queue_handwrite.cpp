// Module 13 题 S7：手撕 SPSC（单生产者-单消费者）无锁队列
// 要求：模板类，环形缓冲区
//       alignas(64) 防止 false sharing
//       memory_order_acquire/release
// 测试：单生产者单消费者 10000 条消息全部正确传递
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <atomic>
#include <array>
#include <thread>
#include <vector>

namespace {

// SPSC 环形缓冲区队列。
// - 容量 Capacity 为槽位数，可用容量为 Capacity-1（保留一个空位区分空/满）。
// - write_pos_ / read_pos_ 用原子变量分别由 producer / consumer 独占更新。
// - 用 alignas(64) 将两个原子隔离到不同缓存行，避免 false sharing。
template <typename T, size_t Capacity>
class SPSCQueue {
public:
    SPSCQueue() : write_pos_(0), read_pos_(0) {}

    SPSCQueue(const SPSCQueue&) = delete;
    SPSCQueue& operator=(const SPSCQueue&) = delete;

    // 非阻塞入队。队满返回 false。
    bool try_push(const T& value) {
        const size_t w = write_pos_.load(std::memory_order_relaxed);
        const size_t next = (w + 1) % Capacity;
        if (next == read_pos_.load(std::memory_order_acquire)) return false;  // 满
        buffer_[w] = value;
        write_pos_.store(next, std::memory_order_release);
        return true;
    }

    // 非阻塞出队。队空返回 false。
    bool try_pop(T& out) {
        const size_t r = read_pos_.load(std::memory_order_relaxed);
        if (r == write_pos_.load(std::memory_order_acquire)) return false;  // 空
        out = buffer_[r];
        read_pos_.store((r + 1) % Capacity, std::memory_order_release);
        return true;
    }

private:
    // alignas(64) 防止 false sharing：两个原子位于不同缓存行。
    alignas(64) std::atomic<size_t> write_pos_;
    alignas(64) std::atomic<size_t> read_pos_;
    alignas(64) std::array<T, Capacity> buffer_{};
};

}  // namespace

// ===== 单元测试 =====

TEST(SpscQueueHandwriteTest, SingleThreadPushPop) {
    SPSCQueue<int, 8> q;
    int v = 0;
    EXPECT_FALSE(q.try_pop(v));  // 空队列
    EXPECT_TRUE(q.try_push(1));
    EXPECT_TRUE(q.try_push(2));
    EXPECT_TRUE(q.try_pop(v)); EXPECT_EQ(v, 1);
    EXPECT_TRUE(q.try_pop(v)); EXPECT_EQ(v, 2);
    EXPECT_FALSE(q.try_pop(v));
}

TEST(SpscQueueHandwriteTest, FullQueueReturnsFalse) {
    SPSCQueue<int, 4> q;  // 可用容量 3
    EXPECT_TRUE(q.try_push(1));
    EXPECT_TRUE(q.try_push(2));
    EXPECT_TRUE(q.try_push(3));
    EXPECT_FALSE(q.try_push(4));  // 满
    int v = 0;
    EXPECT_TRUE(q.try_pop(v)); EXPECT_EQ(v, 1);
    EXPECT_TRUE(q.try_push(4));   // 出队后可再入
}

TEST(SpscQueueHandwriteTest, ProducerConsumerTenThousandMessages) {
    constexpr int N = 10000;
    SPSCQueue<int, 1024> q;  // 容量 1024，远小于 N，测试背压

    std::thread producer([&q, N] {
        for (int i = 0; i < N; ++i) {
            while (!q.try_push(i)) {
                std::this_thread::yield();  // 队满则让出 CPU
            }
        }
    });

    std::vector<int> received;
    received.reserve(N);
    std::thread consumer([&q, &received, N] {
        int v = 0;
        while (static_cast<int>(received.size()) < N) {
            if (q.try_pop(v)) {
                received.push_back(v);
            } else {
                std::this_thread::yield();  // 队空则让出 CPU
            }
        }
    });

    producer.join();
    consumer.join();

    // 验证全部 10000 条消息按序到达
    ASSERT_EQ(received.size(), static_cast<size_t>(N));
    for (int i = 0; i < N; ++i) {
        EXPECT_EQ(received[i], i) << "mismatch at index " << i;
    }
}

TEST(SpscQueueHandwriteTest, ProducerConsumerStructPayload) {
    struct Item {
        int seq;
        long payload;
    };
    constexpr int N = 10000;
    SPSCQueue<Item, 512> q;

    std::thread producer([&q, N] {
        for (int i = 0; i < N; ++i) {
            while (!q.try_push(Item{i, static_cast<long>(i) * 7})) {
                std::this_thread::yield();
            }
        }
    });

    long sum = 0;
    int count = 0;
    std::thread consumer([&q, &sum, &count, N] {
        Item it{};
        while (count < N) {
            if (q.try_pop(it)) {
                sum += it.payload;
                ++count;
            } else {
                std::this_thread::yield();
            }
        }
    });

    producer.join();
    consumer.join();

    EXPECT_EQ(count, N);
    // 7 * (0+1+...+9999) = 7 * 9999*10000/2 = 349965000
    EXPECT_EQ(sum, 7L * 9999L * 10000L / 2L);
}
