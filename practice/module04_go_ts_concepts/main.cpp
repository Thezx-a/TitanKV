/*
 * ============================================================================
 * TitanKV 练习 - Module 04: Go/TS 概念的 C++ 模拟
 * ============================================================================
 * 说明：
 *   正式 Module 04 课程内容是 Go / TypeScript，这里用 C++ 练习同类并发与类型模式：
 *   1) Channel<T>：带 Close 的队列通道（mutex + condition_variable）
 *   2) 用 std::thread 模拟 goroutine，经 Channel 通信
 *   3) 类似 TypeScript 的 Result<T,E> 可辨识联合（Ok / Err）
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module04_go_ts_concepts
 * ============================================================================
 */

#include <cassert>
#include <condition_variable>
#include <iostream>
#include <mutex>
#include <optional>
#include <queue>
#include <string>
#include <thread>
#include <utility>
#include <vector>

// ---------------------------------------------------------------------------
// Channel<T>：简化版 Go channel（有缓冲队列 + Close）
// ---------------------------------------------------------------------------
// Send：关闭后返回 false；未关闭则入队并 notify
// Recv：返回 nullopt 表示通道已关闭且队列为空（类似 Go 的 ok=false）
template <typename T>
class Channel {
 public:
  Channel() : closed_(false) {}

  bool Send(T value) {
    {
      std::lock_guard<std::mutex> lk(mu_);
      if (closed_) return false;
      q_.push(std::move(value));
    }
    cv_.notify_one();
    return true;
  }

  // 阻塞接收；关闭且空时返回 nullopt
  std::optional<T> Recv() {
    std::unique_lock<std::mutex> lk(mu_);
    cv_.wait(lk, [this] { return closed_ || !q_.empty(); });
    if (q_.empty()) {
      return std::nullopt;  // closed
    }
    T v = std::move(q_.front());
    q_.pop();
    return v;
  }

  void Close() {
    {
      std::lock_guard<std::mutex> lk(mu_);
      closed_ = true;
    }
    cv_.notify_all();
  }

 private:
  std::mutex mu_;
  std::condition_variable cv_;
  std::queue<T> q_;
  bool closed_;
};

// ---------------------------------------------------------------------------
// Result<T,E>：TypeScript 风格的 Ok | Err（教学版）
// ---------------------------------------------------------------------------
template <typename T, typename E>
class Result {
 public:
  static Result Ok(T value) {
    Result r;
    r.ok_ = true;
    r.value_ = std::move(value);
    return r;
  }
  static Result Err(E err) {
    Result r;
    r.ok_ = false;
    r.error_ = std::move(err);
    return r;
  }

  bool is_ok() const { return ok_; }
  bool is_err() const { return !ok_; }
  const T& value() const {
    assert(ok_);
    return value_;
  }
  const E& error() const {
    assert(!ok_);
    return error_;
  }

 private:
  Result() = default;
  bool ok_{false};
  T value_{};
  E error_{};
};

// 模拟「解析整数」API：成功返回 Ok，失败返回 Err 字符串
Result<int, std::string> ParseIntLikeTS(const std::string& s) {
  try {
    size_t idx = 0;
    int v = std::stoi(s, &idx);
    if (idx != s.size()) {
      return Result<int, std::string>::Err("trailing junk");
    }
    return Result<int, std::string>::Ok(v);
  } catch (...) {
    return Result<int, std::string>::Err("parse failed");
  }
}

int main() {
  std::cout << "=== Module04: Channel / goroutine 模拟 / Result ===\n";
  std::cout << "（正式课为 Go/TS；此处用 C++ 练习并发通道与类型化结果）\n";

  // ---- Channel + 工作线程（模拟 goroutine）----
  Channel<int> ch;
  std::thread producer([&ch] {
    // 生产者 goroutine：发送 1..5 后关闭
    for (int i = 1; i <= 5; ++i) {
      bool ok = ch.Send(i);
      assert(ok);
    }
    ch.Close();
  });

  std::vector<int> received;
  std::thread consumer([&ch, &received] {
    // 消费者：直到通道关闭且排空
    while (auto v = ch.Recv()) {
      received.push_back(*v);
    }
  });

  producer.join();
  consumer.join();
  assert(received.size() == 5);
  for (int i = 0; i < 5; ++i) {
    assert(received[i] == i + 1);
  }
  // 关闭后再 Send 应失败
  assert(!ch.Send(99));
  std::cout << "[Channel] 多线程收发 + Close 自检通过\n";

  // ---- Result 模式 ----
  auto r1 = ParseIntLikeTS("42");
  assert(r1.is_ok() && r1.value() == 42);
  auto r2 = ParseIntLikeTS("x");
  assert(r2.is_err());
  std::cout << "[Result] Ok/Err 自检通过，err=" << r2.error() << "\n";

  std::cout << "module04_go_ts_concepts SUCCESS\n";
  return 0;
}