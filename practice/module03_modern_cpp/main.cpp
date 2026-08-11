/*
 * ============================================================================
 * TitanKV 练习 - Module 03: 现代 C++（UniquePtr / ThreadPool / SPSC）
 * ============================================================================
 * 目标：
 *   1) 手写 move-only UniquePtr<T>，理解独占所有权与 RAII
 *   2) 简易 ThreadPool：mutex + condition_variable + queue，submit 返回 future
 *   3) SPSC 环形缓冲区：容量为 2 的幂，atomic head/tail
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module03_modern_cpp
 * ============================================================================
 */

#include <atomic>
#include <cassert>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <future>
#include <iostream>
#include <mutex>
#include <queue>
#include <thread>
#include <utility>
#include <vector>

// ---------------------------------------------------------------------------
// UniquePtr<T>：独占智能指针（教学版，不支持数组与自定义删除器）
// ---------------------------------------------------------------------------
template <typename T>
class UniquePtr {
 public:
  explicit UniquePtr(T* p = nullptr) noexcept : ptr_(p) {}
  ~UniquePtr() { reset(); }

  // 禁止拷贝：保证唯一所有者
  UniquePtr(const UniquePtr&) = delete;
  UniquePtr& operator=(const UniquePtr&) = delete;

  // 允许移动：所有权转移
  UniquePtr(UniquePtr&& other) noexcept : ptr_(other.ptr_) { other.ptr_ = nullptr; }
  UniquePtr& operator=(UniquePtr&& other) noexcept {
    if (this != &other) {
      reset();
      ptr_ = other.ptr_;
      other.ptr_ = nullptr;
    }
    return *this;
  }

  T* get() const noexcept { return ptr_; }
  T* release() noexcept {
    T* tmp = ptr_;
    ptr_ = nullptr;
    return tmp;
  }
  void reset(T* p = nullptr) noexcept {
    if (ptr_) delete ptr_;
    ptr_ = p;
  }

  T& operator*() const { return *ptr_; }
  T* operator->() const { return ptr_; }
  explicit operator bool() const noexcept { return ptr_ != nullptr; }

 private:
  T* ptr_;
};

template <typename T, typename... Args>
UniquePtr<T> MakeUnique(Args&&... args) {
  return UniquePtr<T>(new T(std::forward<Args>(args)...));
}

// ---------------------------------------------------------------------------
// ThreadPool：固定工作线程 + 任务队列
// ---------------------------------------------------------------------------
// submit 用 packaged_task 包装可调用对象，返回 future，调用方可异步取结果
class ThreadPool {
 public:
  explicit ThreadPool(size_t n) : stop_(false) {
    if (n == 0) n = 1;
    workers_.reserve(n);
    for (size_t i = 0; i < n; ++i) {
      workers_.emplace_back([this] { workerLoop(); });
    }
  }

  ~ThreadPool() {
    {
      std::lock_guard<std::mutex> lk(mu_);
      stop_ = true;
    }
    cv_.notify_all();
    for (auto& t : workers_) {
      if (t.joinable()) t.join();
    }
  }

  ThreadPool(const ThreadPool&) = delete;
  ThreadPool& operator=(const ThreadPool&) = delete;

  template <typename F>
  auto submit(F&& f) -> std::future<typename std::result_of<F()>::type> {
    using R = typename std::result_of<F()>::type;
    auto task = std::make_shared<std::packaged_task<R()>>(std::forward<F>(f));
    std::future<R> fut = task->get_future();
    {
      std::lock_guard<std::mutex> lk(mu_);
      assert(!stop_);
      tasks_.emplace([task]() { (*task)(); });
    }
    cv_.notify_one();
    return fut;
  }

 private:
  void workerLoop() {
    for (;;) {
      std::function<void()> job;
      {
        std::unique_lock<std::mutex> lk(mu_);
        cv_.wait(lk, [this] { return stop_ || !tasks_.empty(); });
        if (stop_ && tasks_.empty()) return;
        job = std::move(tasks_.front());
        tasks_.pop();
      }
      job();
    }
  }

  std::vector<std::thread> workers_;
  std::queue<std::function<void()>> tasks_;
  std::mutex mu_;
  std::condition_variable cv_;
  bool stop_;
};

// ---------------------------------------------------------------------------
// SpscRingBuffer：单生产者单消费者无锁环形队列（容量必须是 2 的幂）
// ---------------------------------------------------------------------------
// 用 head_/tail_ 原子变量区分空/满：
// - 空：head == tail
// - 满：(tail + 1) & mask == head（刻意浪费一个槽位区分满空）
template <typename T, size_t Capacity>
class SpscRingBuffer {
  static_assert((Capacity & (Capacity - 1)) == 0, "Capacity must be power of 2");
  static_assert(Capacity >= 2, "Capacity too small");

 public:
  SpscRingBuffer() : head_(0), tail_(0) {}

  bool push(const T& item) {
    const size_t t = tail_.load(std::memory_order_relaxed);
    const size_t next = (t + 1) & kMask;
    if (next == head_.load(std::memory_order_acquire)) {
      return false;  // 满
    }
    buf_[t] = item;
    tail_.store(next, std::memory_order_release);
    return true;
  }

  bool pop(T& out) {
    const size_t h = head_.load(std::memory_order_relaxed);
    if (h == tail_.load(std::memory_order_acquire)) {
      return false;  // 空
    }
    out = buf_[h];
    head_.store((h + 1) & kMask, std::memory_order_release);
    return true;
  }

 private:
  static constexpr size_t kMask = Capacity - 1;
  T buf_[Capacity];
  // 缓存行填充可减少伪共享，教学版省略
  std::atomic<size_t> head_;
  std::atomic<size_t> tail_;
};

int main() {
  std::cout << "=== Module03: UniquePtr / ThreadPool / SPSC ===\n";

  // ---- UniquePtr ----
  {
    UniquePtr<int> p = MakeUnique<int>(42);
    assert(p && *p == 42);
    UniquePtr<int> q = std::move(p);
    assert(!p);
    assert(q && *q == 42);
    std::cout << "[UniquePtr] 移动语义自检通过\n";
  }

  // ---- ThreadPool ----
  {
    ThreadPool pool(4);
    std::vector<std::future<int>> futs;
    for (int i = 0; i < 8; ++i) {
      futs.push_back(pool.submit([i]() { return i * i; }));
    }
    for (int i = 0; i < 8; ++i) {
      assert(futs[i].get() == i * i);
    }
    std::cout << "[ThreadPool] submit/future 自检通过\n";
  }

  // ---- SPSC ring ----
  {
    SpscRingBuffer<int, 8> ring;
    std::atomic<bool> done{false};
    std::thread prod([&] {
      for (int i = 0; i < 100; ++i) {
        while (!ring.push(i)) {
          std::this_thread::yield();
        }
      }
      done.store(true, std::memory_order_release);
    });
    std::thread cons([&] {
      int got = 0;
      int expect = 0;
      while (expect < 100) {
        int v;
        if (ring.pop(v)) {
          assert(v == expect);
          ++expect;
          ++got;
        } else if (done.load(std::memory_order_acquire) && expect >= 100) {
          break;
        } else {
          std::this_thread::yield();
        }
      }
      assert(got == 100);
    });
    prod.join();
    cons.join();
    std::cout << "[SPSC] 100 次顺序收发自检通过\n";
  }

  std::cout << "module03_modern_cpp SUCCESS\n";
  return 0;
}