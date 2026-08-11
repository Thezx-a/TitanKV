// Module 13 题25：手撕 LRU Cache（参考 LeetCode 146）
// 要求：哈希表 + 双向链表，get/put O(1)
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <list>
#include <optional>
#include <unordered_map>

namespace {

// LRU Cache：get/put 平均 O(1)。
// 使用 std::list 作为双向链表（O(1) 摘除/置顶），unordered_map 映射 key->迭代器。
template <typename K, typename V>
class LRUCache {
public:
    explicit LRUCache(size_t capacity) : capacity_(capacity) {}

    // 命中返回 value 并提升到队首；未命中返回 nullopt。
    std::optional<V> get(const K& key) {
        auto it = map_.find(key);
        if (it == map_.end()) return std::nullopt;
        // 把节点移到队首（最新使用）。
        list_.splice(list_.begin(), list_, it->second);
        return it->second->second;
    }

    // 插入或更新 key->value。若超容量，淘汰队尾（最久未使用）。
    void put(const K& key, const V& value) {
        auto it = map_.find(key);
        if (it != map_.end()) {
            it->second->second = value;             // 更新 value
            list_.splice(list_.begin(), list_, it->second);  // 提升到队首
            return;
        }
        if (list_.size() >= capacity_) {
            auto& back = list_.back();
            map_.erase(back.first);
            list_.pop_back();
        }
        list_.emplace_front(key, value);
        map_[key] = list_.begin();
    }

    size_t size() const { return list_.size(); }

private:
    using ListType = std::list<std::pair<K, V>>;
    ListType list_;
    std::unordered_map<K, typename ListType::iterator> map_;
    size_t capacity_;
};

}  // namespace

// ===== 单元测试（参考 LeetCode 146 经典用例） =====

TEST(LRUHandwriteTest, LeetCode146Example) {
    LRUCache<int, int> lru(2);
    lru.put(1, 1);                       // cache = {1=1}
    lru.put(2, 2);                       // cache = {1=1, 2=2}
    EXPECT_EQ(lru.get(1).value(), 1);    // 返回 1；cache = {2=2, 1=1}
    lru.put(3, 3);                       // 淘汰 key 2，cache = {1=1, 3=3}
    EXPECT_FALSE(lru.get(2).has_value());// 返回 -1（未找到）
    lru.put(4, 4);                       // 淘汰 key 1，cache = {3=3, 4=4}
    EXPECT_FALSE(lru.get(1).has_value());// 返回 -1
    EXPECT_EQ(lru.get(3).value(), 3);    // 返回 3
    EXPECT_EQ(lru.get(4).value(), 4);    // 返回 4
}

TEST(LRUHandwriteTest, UpdateExistingKeyDoesNotGrowSize) {
    LRUCache<int, int> lru(2);
    lru.put(1, 1);
    lru.put(2, 2);
    lru.put(1, 10);                      // 更新 key=1
    EXPECT_EQ(lru.size(), 2u);
    EXPECT_EQ(lru.get(1).value(), 10);
    // 更新后 key=1 最新，下次应淘汰 key=2
    lru.put(3, 3);
    EXPECT_FALSE(lru.get(2).has_value());
    EXPECT_EQ(lru.get(1).value(), 10);
    EXPECT_EQ(lru.get(3).value(), 3);
}

TEST(LRUHandwriteTest, GetPromotesToMostRecent) {
    LRUCache<int, int> lru(3);
    lru.put(1, 1);
    lru.put(2, 2);
    lru.put(3, 3);
    // 访问 key=1，提升；现在 LRU 顺序尾部应为 key=2
    EXPECT_EQ(lru.get(1).value(), 1);
    lru.put(4, 4);                       // 淘汰 key=2
    EXPECT_FALSE(lru.get(2).has_value());
    EXPECT_EQ(lru.get(1).value(), 1);
    EXPECT_EQ(lru.get(3).value(), 3);
    EXPECT_EQ(lru.get(4).value(), 4);
}

TEST(LRUHandwriteTest, CapacityOneEdgeCase) {
    LRUCache<int, int> lru(1);
    lru.put(1, 1);
    EXPECT_EQ(lru.get(1).value(), 1);
    lru.put(2, 2);                       // 淘汰 key=1
    EXPECT_FALSE(lru.get(1).has_value());
    EXPECT_EQ(lru.get(2).value(), 2);
}

TEST(LRUHandwriteTest, MissingKeyReturnsNullopt) {
    LRUCache<int, int> lru(2);
    EXPECT_FALSE(lru.get(42).has_value());
    lru.put(1, 1);
    EXPECT_FALSE(lru.get(99).has_value());
}

TEST(LRUHandwriteTest, StringKeyValue) {
    LRUCache<std::string, std::string> lru(2);
    lru.put("a", "alpha");
    lru.put("b", "beta");
    EXPECT_EQ(lru.get("a").value(), "alpha");
    lru.put("c", "gamma");  // 淘汰 "b"
    EXPECT_FALSE(lru.get("b").has_value());
    EXPECT_EQ(lru.get("a").value(), "alpha");
    EXPECT_EQ(lru.get("c").value(), "gamma");
}
