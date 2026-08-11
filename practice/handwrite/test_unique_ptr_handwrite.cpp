// Module 13 题 S4：手撕 unique_ptr
// 要求：模板类，移动语义，禁用拷贝
//       operator*, operator->, get, release, reset
// 测试：移动构造、release、reset、自定义 deleter
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <cstddef>
#include <memory>
#include <utility>

namespace {

// 简化版 unique_ptr，默认用 delete 释放；支持自定义 deleter。
template <typename T, typename Deleter = std::default_delete<T>>
class MyUniquePtr {
public:
    MyUniquePtr() noexcept : ptr_(nullptr), deleter_() {}
    MyUniquePtr(std::nullptr_t) noexcept : ptr_(nullptr), deleter_() {}
    explicit MyUniquePtr(T* p) noexcept : ptr_(p), deleter_() {}
    MyUniquePtr(T* p, Deleter d) noexcept : ptr_(p), deleter_(std::move(d)) {}

    // 禁用拷贝
    MyUniquePtr(const MyUniquePtr&) = delete;
    MyUniquePtr& operator=(const MyUniquePtr&) = delete;

    // 移动构造
    MyUniquePtr(MyUniquePtr&& other) noexcept
        : ptr_(other.release()), deleter_(std::move(other.deleter_)) {}

    // 移动赋值
    MyUniquePtr& operator=(MyUniquePtr&& other) noexcept {
        if (this != &other) {
            reset(other.release());
            deleter_ = std::move(other.deleter_);
        }
        return *this;
    }

    ~MyUniquePtr() { reset(); }

    T& operator*() const { return *ptr_; }
    T* operator->() const { return ptr_; }
    T* get() const { return ptr_; }
    explicit operator bool() const { return ptr_ != nullptr; }

    // 释放所有权，返回原始指针
    T* release() {
        T* p = ptr_;
        ptr_ = nullptr;
        return p;
    }

    // 重置：删除旧资源并接管新指针
    void reset(T* p = nullptr) {
        if (ptr_ != nullptr) {
            deleter_(ptr_);
        }
        ptr_ = p;
    }

private:
    T* ptr_;
    Deleter deleter_;
};

}  // namespace

// ===== 单元测试 =====

namespace {
struct Tracker {
    int value;
    bool* deleted;
    Tracker(int v, bool* d) : value(v), deleted(d) {}
    ~Tracker() { if (deleted) *deleted = true; }
};
}  // namespace

TEST(UniquePtrHandwriteTest, ConstructAndDereference) {
    bool deleted = false;
    {
        MyUniquePtr<Tracker> p(new Tracker(42, &deleted));
        ASSERT_TRUE(p);
        EXPECT_EQ(p->value, 42);
        EXPECT_EQ((*p).value, 42);
        EXPECT_NE(p.get(), nullptr);
    }
    EXPECT_TRUE(deleted);
}

TEST(UniquePtrHandwriteTest, MoveConstructor) {
    bool deleted = false;
    MyUniquePtr<Tracker> a(new Tracker(7, &deleted));
    Tracker* raw = a.get();
    MyUniquePtr<Tracker> b(std::move(a));
    EXPECT_FALSE(a);            // a 已被置空
    EXPECT_EQ(b.get(), raw);    // b 接管资源
    EXPECT_FALSE(deleted);      // 资源未释放
    EXPECT_EQ(b->value, 7);
}

TEST(UniquePtrHandwriteTest, MoveAssignment) {
    bool d1 = false, d2 = false;
    MyUniquePtr<Tracker> a(new Tracker(1, &d1));
    MyUniquePtr<Tracker> b(new Tracker(2, &d2));
    b = std::move(a);           // 释放 b 的资源，接管 a 的资源
    EXPECT_TRUE(d2);            // b 原资源被释放
    EXPECT_FALSE(a);
    EXPECT_EQ(b->value, 1);
}

TEST(UniquePtrHandwriteTest, ReleaseTransfersOwnership) {
    bool deleted = false;
    Tracker* raw = nullptr;
    {
        MyUniquePtr<Tracker> p(new Tracker(99, &deleted));
        raw = p.release();      // 释放所有权
        EXPECT_FALSE(p);       // p 现在为空
        EXPECT_FALSE(deleted); // 资源未被删除
    }
    EXPECT_FALSE(deleted);     // 出作用域仍未删除
    delete raw;                 // 手动释放
}

TEST(UniquePtrHandwriteTest, ResetDeletesAndReplaces) {
    bool d1 = false, d2 = false;
    MyUniquePtr<Tracker> p(new Tracker(1, &d1));
    p.reset(new Tracker(2, &d2));
    EXPECT_TRUE(d1);           // 旧资源被删除
    EXPECT_FALSE(d2);          // 新资源仍在
    EXPECT_EQ(p->value, 2);
    p.reset();                 // 显式置空
    EXPECT_TRUE(d2);
    EXPECT_FALSE(p);
}

TEST(UniquePtrHandwriteTest, ResetWithNullptrIsSafeOnEmpty) {
    MyUniquePtr<Tracker> p;
    p.reset(nullptr);          // 不应崩溃
    EXPECT_FALSE(p);
}

// 自定义 deleter：记录调用
struct CountingDeleter {
    int* count;
    explicit CountingDeleter(int* c) : count(c) {}
    void operator()(Tracker* p) {
        ++(*count);
        delete p;
    }
};

TEST(UniquePtrHandwriteTest, CustomDeleterInvoked) {
    int count = 0;
    bool deleted = false;
    {
        MyUniquePtr<Tracker, CountingDeleter> p(new Tracker(5, &deleted),
                                                CountingDeleter(&count));
        EXPECT_EQ(p->value, 5);
    }
    EXPECT_EQ(count, 1);
    EXPECT_TRUE(deleted);
}

TEST(UniquePtrHandwriteTest, ArraySpecializationNotRequired) {
    // 本实现未特化数组版本，仅验证单对象路径已正确。
    bool deleted = false;
    auto p = MyUniquePtr<Tracker>(new Tracker(0, &deleted));
    EXPECT_TRUE(p);
}
