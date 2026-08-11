// Module 05 题1 + Module 13 题24：手撕跳表
// 要求：最大层数 16，随机层数 p=0.5，实现 search/insert/erase
// 测试：插入 1-100000，查询 50000 命中、50000 不命中
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <random>
#include <vector>

namespace {

// 最大层数。层数从 1 开始计数，最大为 kMaxLevel。
constexpr int kMaxLevel = 16;
// 晋升概率 p=0.5：每多一层的概率为 0.5。
constexpr double kProbability = 0.5;

// 简易跳表实现（key=int, value=int）。
// 每个节点维护一个 forward 数组，forward[i] 指向同层下一节点。
class SkipList {
public:
    SkipList() : head_(makeNode(kMaxLevel, 0, 0)), rng_(0x5DEECE66D) {}

    ~SkipList() {
        Node* cur = head_->forward[0];
        while (cur != nullptr) {
            Node* nxt = cur->forward[0];
            delete cur;
            cur = nxt;
        }
        delete head_;
    }

    SkipList(const SkipList&) = delete;
    SkipList& operator=(const SkipList&) = delete;

    // 查询 key 是否存在；若存在写出 value 到 out。
    bool search(int key, int* out = nullptr) const {
        const Node* cur = head_;
        for (int i = level_ - 1; i >= 0; --i) {
            while (cur->forward[i] != nullptr && cur->forward[i]->key < key) {
                cur = cur->forward[i];
            }
        }
        cur = cur->forward[0];
        if (cur != nullptr && cur->key == key) {
            if (out != nullptr) *out = cur->value;
            return true;
        }
        return false;
    }

    // 插入 key/value。若 key 已存在则更新 value。
    void insert(int key, int value) {
        std::vector<Node*> update(kMaxLevel, head_);
        Node* cur = head_;
        for (int i = level_ - 1; i >= 0; --i) {
            while (cur->forward[i] != nullptr && cur->forward[i]->key < key) {
                cur = cur->forward[i];
            }
            update[i] = cur;
        }
        cur = cur->forward[0];
        if (cur != nullptr && cur->key == key) {
            cur->value = value;  // 更新
            return;
        }
        const int lvl = randomLevel();
        if (lvl > level_) {
            for (int i = level_; i < lvl; ++i) update[i] = head_;
            level_ = lvl;
        }
        Node* node = makeNode(lvl, key, value);
        for (int i = 0; i < lvl; ++i) {
            node->forward[i] = update[i]->forward[i];
            update[i]->forward[i] = node;
        }
    }

    // 删除 key。返回是否真的删除了。
    bool erase(int key) {
        std::vector<Node*> update(kMaxLevel, head_);
        Node* cur = head_;
        for (int i = level_ - 1; i >= 0; --i) {
            while (cur->forward[i] != nullptr && cur->forward[i]->key < key) {
                cur = cur->forward[i];
            }
            update[i] = cur;
        }
        cur = cur->forward[0];
        if (cur == nullptr || cur->key != key) return false;
        for (int i = 0; i < level_; ++i) {
            if (update[i]->forward[i] != cur) break;
            update[i]->forward[i] = cur->forward[i];
        }
        delete cur;
        while (level_ > 1 && head_->forward[level_ - 1] == nullptr) {
            --level_;
        }
        return true;
    }

private:
    struct Node {
        int key;
        int value;
        int level;
        Node** forward;  // 长度为 level 的指针数组
        Node(int lvl, int k, int v) : key(k), value(v), level(lvl) {
            forward = new Node*[lvl];
            for (int i = 0; i < lvl; ++i) forward[i] = nullptr;
        }
        ~Node() { delete[] forward; }
    };

    static Node* makeNode(int lvl, int k, int v) { return new Node(lvl, k, v); }

    // 几何分布生成层数：p=0.5，最大 kMaxLevel。
    int randomLevel() {
        int lvl = 1;
        while (dist_(rng_) < kProbability && lvl < kMaxLevel) ++lvl;
        return lvl;
    }

    Node* head_;
    int level_ = 1;
    std::mt19937 rng_;
    std::uniform_real_distribution<double> dist_{0.0, 1.0};
};

}  // namespace

// ===== 单元测试 =====

TEST(SkipListHandwriteTest, InsertAndSearchSmall) {
    SkipList sl;
    sl.insert(3, 30);
    sl.insert(1, 10);
    sl.insert(2, 20);
    int v = 0;
    ASSERT_TRUE(sl.search(1, &v)); EXPECT_EQ(v, 10);
    ASSERT_TRUE(sl.search(2, &v)); EXPECT_EQ(v, 20);
    ASSERT_TRUE(sl.search(3, &v)); EXPECT_EQ(v, 30);
    EXPECT_FALSE(sl.search(4));
}

TEST(SkipListHandwriteTest, UpdateExistingKey) {
    SkipList sl;
    sl.insert(7, 70);
    sl.insert(7, 77);
    int v = 0;
    ASSERT_TRUE(sl.search(7, &v));
    EXPECT_EQ(v, 77);
}

TEST(SkipListHandwriteTest, EraseTest) {
    SkipList sl;
    for (int i = 0; i < 100; ++i) sl.insert(i, i * 10);
    EXPECT_TRUE(sl.erase(50));
    EXPECT_FALSE(sl.search(50));
    EXPECT_FALSE(sl.erase(50));  // 再次删除应失败
    EXPECT_TRUE(sl.erase(0));
    EXPECT_TRUE(sl.erase(99));
    EXPECT_FALSE(sl.search(0));
    EXPECT_FALSE(sl.search(99));
    // 中间元素仍在
    int v = 0;
    ASSERT_TRUE(sl.search(50 - 1, &v)); EXPECT_EQ(v, 490);
}

TEST(SkipListHandwriteTest, InsertHundredThousandQueryHitMiss) {
    SkipList sl;
    constexpr int N = 100000;
    for (int i = 1; i <= N; ++i) sl.insert(i, i * 2);

    // 50000 命中：1, 3, 5, ..., 99999
    int hits = 0;
    int v = 0;
    for (int i = 1; i <= N; i += 2) {
        if (sl.search(i, &v) && v == i * 2) ++hits;
    }
    EXPECT_EQ(hits, 50000);

    // 50000 不命中：N+1 .. N+50000
    int misses = 0;
    for (int i = N + 1; i <= N + 50000; ++i) {
        if (!sl.search(i)) ++misses;
    }
    EXPECT_EQ(misses, 50000);
}
