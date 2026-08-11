/*
 * ============================================================================
 * TitanKV 练习 - Module 05: SkipList（跳表）
 * ============================================================================
 * 目标：
 *   实现 SkipList<int>：p=0.5，maxLevel=16，支持 search / insert / erase
 *   理解 randomLevel 与 update[] 前驱数组在插入/删除中的作用
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module05_skiplist
 * ============================================================================
 */

#include <cassert>
#include <cstdlib>
#include <ctime>
#include <iostream>
#include <vector>

// ---------------------------------------------------------------------------
// SkipList：多层有序链表，期望 O(log n) 查找
// ---------------------------------------------------------------------------
// 要点：
 // 1) randomLevel：以概率 p=0.5 抬高层数，模拟几何分布，期望层高为常数
 // 2) update[i]：查找过程中记录「第 i 层小于目标 key 的最右结点」
 //    插入/删除时只需修改 update[i]->forward[i]，无需再扫一遍
template <typename Key>
class SkipList {
 public:
  static constexpr int kMaxLevel = 16;
  static constexpr double kP = 0.5;

  struct Node {
    Key key;
    std::vector<Node*> forward;  // forward[i] = 第 i 层后继
    Node(Key k, int level) : key(std::move(k)), forward(level, nullptr) {}
  };

  SkipList() : level_(1) {
    head_ = new Node(Key{}, kMaxLevel);
    std::srand(static_cast<unsigned>(std::time(nullptr)));
  }

  ~SkipList() {
    Node* cur = head_->forward[0];
    while (cur) {
      Node* nxt = cur->forward[0];
      delete cur;
      cur = nxt;
    }
    delete head_;
  }

  SkipList(const SkipList&) = delete;
  SkipList& operator=(const SkipList&) = delete;

  bool search(const Key& key) const {
    Node* x = head_;
    for (int i = level_ - 1; i >= 0; --i) {
      while (x->forward[i] && x->forward[i]->key < key) {
        x = x->forward[i];
      }
    }
    x = x->forward[0];
    return x != nullptr && x->key == key;
  }

  int randomLevel() const {
    int lvl = 1;
    // 每次“抛硬币”成功则层高 +1，直到失败或触顶
    while ((std::rand() / static_cast<double>(RAND_MAX)) < kP && lvl < kMaxLevel) {
      ++lvl;
    }
    return lvl;
  }

  bool insert(const Key& key) {
    Node* update[kMaxLevel];
    for (int i = 0; i < kMaxLevel; ++i) update[i] = nullptr;

    Node* x = head_;
    for (int i = level_ - 1; i >= 0; --i) {
      while (x->forward[i] && x->forward[i]->key < key) {
        x = x->forward[i];
      }
      update[i] = x;  // 记录前驱
    }
    x = x->forward[0];
    if (x && x->key == key) return false;

    int lvl = randomLevel();
    if (lvl > level_) {
      for (int i = level_; i < lvl; ++i) update[i] = head_;
      level_ = lvl;
    }

    Node* node = new Node(key, lvl);
    for (int i = 0; i < lvl; ++i) {
      node->forward[i] = update[i]->forward[i];
      update[i]->forward[i] = node;
    }
    return true;
  }

  bool erase(const Key& key) {
    Node* update[kMaxLevel];
    for (int i = 0; i < kMaxLevel; ++i) update[i] = nullptr;

    Node* x = head_;
    for (int i = level_ - 1; i >= 0; --i) {
      while (x->forward[i] && x->forward[i]->key < key) {
        x = x->forward[i];
      }
      update[i] = x;
    }
    x = x->forward[0];
    if (!x || x->key != key) return false;

    for (int i = 0; i < level_; ++i) {
      if (update[i]->forward[i] != x) break;
      update[i]->forward[i] = x->forward[i];
    }
    delete x;
    while (level_ > 1 && head_->forward[level_ - 1] == nullptr) --level_;
    return true;
  }

 private:
  Node* head_;
  int level_;
};

int main() {
  std::cout << "=== Module05: SkipList ===\n";

  SkipList<int> sl;
  for (int i = 1; i <= 20; ++i) {
    assert(sl.insert(i));
  }
  assert(!sl.insert(10));  // 重复插入失败

  for (int i = 1; i <= 20; ++i) {
    assert(sl.search(i));
  }
  assert(!sl.search(0));
  assert(!sl.search(21));

  assert(sl.erase(1));
  assert(sl.erase(10));
  assert(sl.erase(20));
  assert(!sl.erase(10));
  assert(!sl.search(1));
  assert(!sl.search(10));
  assert(!sl.search(20));
  assert(sl.search(2));
  assert(sl.search(19));

  std::cout << "[SkipList] insert 1..20 / erase / search 自检通过\n";
  std::cout << "module05_skiplist SUCCESS\n";
  return 0;
}