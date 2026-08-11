// Module 13: 面试手写 — LRU Cache (LC146) + 线程安全计数器示意
#include <cassert>
#include <iostream>
#include <list>
#include <mutex>
#include <string>
#include <unordered_map>
#include <utility>
#include <vector>

// LRU：哈希表 + 双向链表，get/put 均摊 O(1)
class LRUCache {
 public:
  explicit LRUCache(int capacity) : cap_(capacity) {}

  int get(int key) {
    auto it = map_.find(key);
    if (it == map_.end()) return -1;
    // 移到链表头部表示最近使用
    list_.splice(list_.begin(), list_, it->second);
    return it->second->second;
  }

  void put(int key, int value) {
    auto it = map_.find(key);
    if (it != map_.end()) {
      it->second->second = value;
      list_.splice(list_.begin(), list_, it->second);
      return;
    }
    if ((int)list_.size() >= cap_) {
      auto& old = list_.back();
      map_.erase(old.first);
      list_.pop_back();
    }
    list_.emplace_front(key, value);
    map_[key] = list_.begin();
  }

  // 从新到旧的 key 序列（用于断言淘汰顺序）
  std::vector<int> keys_mru_to_lru() const {
    std::vector<int> v;
    for (auto& p : list_) v.push_back(p.first);
    return v;
  }

 private:
  int cap_;
  std::list<std::pair<int, int>> list_;
  std::unordered_map<int, std::list<std::pair<int, int>>::iterator> map_;
};

// 其他常考手写：线程安全计数器（示意，非完整并发容器）
class AtomicCounter {
 public:
  void inc() { std::lock_guard<std::mutex> g(mu_); ++n_; }
  long get() const { std::lock_guard<std::mutex> g(mu_); return n_; }
 private:
  mutable std::mutex mu_;
  long n_ = 0;
};

int main() {
  std::cout << "==== module13_interview ====\n";
  LRUCache cache(2);
  cache.put(1, 1);
  cache.put(2, 2);
  assert(cache.get(1) == 1);
  cache.put(3, 3);  // 淘汰 key 2
  assert(cache.get(2) == -1);
  assert(cache.get(3) == 3);
  cache.put(4, 4);  // 淘汰 key 1
  assert(cache.get(1) == -1);
  assert(cache.get(3) == 3);
  assert(cache.get(4) == 4);
  std::cout << "[OK] LRU basic LC146\n";

  // 压力：容量 3，连续 put 验证淘汰顺序
  LRUCache big(3);
  for (int i = 0; i < 10; ++i) big.put(i, i * 10);
  auto keys = big.keys_mru_to_lru();
  assert(keys.size() == 3);
  assert(keys[0] == 9 && keys[1] == 8 && keys[2] == 7);
  assert(big.get(7) == 70);
  keys = big.keys_mru_to_lru();
  assert(keys[0] == 7);
  std::cout << "[OK] LRU stress eviction order\n";

  AtomicCounter ctr;
  ctr.inc(); ctr.inc();
  assert(ctr.get() == 2);
  std::cout << "[OK] thread-safe counter sketch (also: 手写题常见还有单例/读写锁/阻塞队列等)\n";
  std::cout << "ALL CHECKS PASSED\n";
  return 0;
}