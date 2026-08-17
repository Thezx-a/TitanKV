#pragma once
#include <coroutine>
#include <exception>

namespace skynet {
namespace core {

// Fire-and-forget coroutine: auto-starts, self-destroys on completion.
struct DetachedTask {
    struct promise_type {
        DetachedTask get_return_object() { return DetachedTask{}; }
        std::suspend_never initial_suspend() noexcept { return {}; }
        struct FinalAwaiter {
            bool await_ready() noexcept { return false; }
            void await_suspend(std::coroutine_handle<promise_type> h) noexcept { h.destroy(); }
            void await_resume() noexcept {}
        };
        FinalAwaiter final_suspend() noexcept { return {}; }
        void return_void() noexcept {}
        void unhandled_exception() { std::terminate(); }
    };
};

}  // namespace core
}  // namespace skynet
