// =============================================================================
// Module 09: C++20 协程 + Linux epoll 练习
// A) 最小 Task<int> 协程；B) pipe + epoll 就绪演示（带超时，绝不挂死）
// =============================================================================
#include <cassert>
#include <exception>
#include <string>
#include <coroutine>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <utility>

// ============================ A) 最小协程 Task ============================
template <typename T>
struct Task {
  struct promise_type {
    T value{};
    Task get_return_object() {
      return Task{std::coroutine_handle<promise_type>::from_promise(*this)};
    }
    std::suspend_never initial_suspend() noexcept { return {}; }
    std::suspend_always final_suspend() noexcept { return {}; }
    void return_value(T v) { value = std::move(v); }
    void unhandled_exception() { std::terminate(); }
  };

  std::coroutine_handle<promise_type> h{};
  explicit Task(std::coroutine_handle<promise_type> handle) : h(handle) {}
  Task(Task&& o) noexcept : h(std::exchange(o.h, {})) {}
  Task(const Task&) = delete;
  ~Task() {
    if (h) h.destroy();
  }

  // 简单 awaiter：挂起当前，恢复子协程，子完成后把结果传回
  bool await_ready() const noexcept { return h && h.done(); }
  void await_suspend(std::coroutine_handle<> cont) const {
    // 先跑完子协程（我们的 Task 使用 initial_suspend=never，通常已跑完）
    // 若未完成则 resume；完成后回到续体
    if (h && !h.done()) h.resume();
    cont.resume();
  }
  T await_resume() const { return h.promise().value; }

  T get() {
    while (h && !h.done()) h.resume();
    return h.promise().value;
  }
};

static Task<int> add_one(int x) {
  co_return x + 1;
}

static Task<int> add_chain(int x) {
  // co_await 另一个 Task，演示链式组合
  int y = co_await add_one(x);
  int z = co_await add_one(y);
  co_return z;
}

static void demo_coroutines() {
  auto t = add_chain(40);
  int r = t.get();
  assert(r == 42);
  std::cout << "[OK] coroutine Task chain: 40 -> " << r << "\n";
}

// ============================ B) epoll + pipe ============================
#if defined(__linux__)
#include <errno.h>
#include <fcntl.h>
#include <sys/epoll.h>
#include <unistd.h>

static void set_nonblock(int fd) {
  int flags = fcntl(fd, F_GETFL, 0);
  assert(flags >= 0);
  assert(fcntl(fd, F_SETFL, flags | O_NONBLOCK) == 0);
}

static void demo_epoll() {
  int fds[2];
  assert(pipe(fds) == 0);
  int rfd = fds[0], wfd = fds[1];
  set_nonblock(rfd);
  set_nonblock(wfd);

  int ep = epoll_create1(0);
  assert(ep >= 0);

  // 使用水平触发(LT)，简单可靠；ET 亦可，这里以演示就绪为主
  epoll_event ev{};
  ev.events = EPOLLIN;  // LT
  ev.data.fd = rfd;
  assert(epoll_ctl(ep, EPOLL_CTL_ADD, rfd, &ev) == 0);

  const char msg[] = "echo-ping";
  ssize_t wn = write(wfd, msg, sizeof(msg) - 1);
  assert(wn == static_cast<ssize_t>(sizeof(msg) - 1));

  epoll_event out[4];
  // 超时 200ms，保证不会永远阻塞
  int n = epoll_wait(ep, out, 4, 200);
  assert(n == 1);
  assert(out[0].data.fd == rfd);
  assert(out[0].events & EPOLLIN);

  char buf[64]{};
  ssize_t rn = read(rfd, buf, sizeof(buf) - 1);
  assert(rn == wn);
  assert(std::string(buf) == msg);

  // 再 wait 一次应超时返回 0
  n = epoll_wait(ep, out, 4, 50);
  assert(n == 0);

  close(ep);
  close(rfd);
  close(wfd);
  std::cout << "[OK] epoll LT + pipe readiness (with timeout)\n";
}
#else
static void demo_epoll() {
  std::cout << "[SKIP] epoll demo (not Linux)\n";
}
#endif

int main() {
  std::cout << "==== module09_epoll_coro ====\n";
  demo_coroutines();
  demo_epoll();
  std::cout << "ALL CHECKS PASSED\n";
  return 0;
}