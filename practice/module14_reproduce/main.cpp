// Module 14: TitanKV smoke test — SkipList + BloomFilter + InternalKey (MVCC-style)
// Course goal: reproduce full TitanKV end-to-end; this binary validates core building blocks.
#include <cassert>
#include <cstdint>
#include <iostream>
#include <optional>
#include <string>
#include <unordered_map>
#include <vector>

#if defined(_WIN32)
#include <windows.h>
#endif

namespace {

void SetupConsoleUtf8() {
#if defined(_WIN32)
  SetConsoleOutputCP(CP_UTF8);
  SetConsoleCP(CP_UTF8);
#endif
}

// ---- Minimal SkipList (MemTable) ----
struct SkipNode {
  std::string key;
  std::string value;
  std::vector<SkipNode*> next;
  SkipNode(std::string k, std::string v, int level)
      : key(std::move(k)), value(std::move(v)), next(level, nullptr) {}
};

class SkipList {
 public:
  explicit SkipList(int max_level = 8) : max_level_(max_level), level_(1) {
    head_ = new SkipNode("", "", max_level_);
  }
  ~SkipList() {
    SkipNode* cur = head_;
    while (cur) {
      SkipNode* n = cur->next[0];
      delete cur;
      cur = n;
    }
  }
  SkipList(const SkipList&) = delete;
  SkipList& operator=(const SkipList&) = delete;

  void Put(const std::string& key, const std::string& value) {
    std::vector<SkipNode*> update(max_level_, nullptr);
    SkipNode* x = head_;
    for (int i = level_ - 1; i >= 0; --i) {
      while (x->next[i] && x->next[i]->key < key) x = x->next[i];
      update[i] = x;
    }
    x = x->next[0];
    if (x && x->key == key) {
      x->value = value;
      return;
    }
    int lvl = RandomLevel();
    if (lvl > level_) {
      for (int i = level_; i < lvl; ++i) update[i] = head_;
      level_ = lvl;
    }
    auto* n = new SkipNode(key, value, lvl);
    for (int i = 0; i < lvl; ++i) {
      n->next[i] = update[i]->next[i];
      update[i]->next[i] = n;
    }
  }

  std::optional<std::string> Get(const std::string& key) const {
    SkipNode* x = head_;
    for (int i = level_ - 1; i >= 0; --i) {
      while (x->next[i] && x->next[i]->key < key) x = x->next[i];
    }
    x = x->next[0];
    if (x && x->key == key) return x->value;
    return std::nullopt;
  }

 private:
  int RandomLevel() {
    int lvl = 1;
    while (lvl < max_level_ && (rng_ = rng_ * 1103515245u + 12345u) % 4 == 0) ++lvl;
    return lvl;
  }

  int max_level_, level_;
  SkipNode* head_;
  uint32_t rng_ = 1;
};

// ---- Simple Bloom Filter ----
class BloomFilter {
 public:
  BloomFilter(size_t bits, int hashes) : bits_(bits, false), k_(hashes) {}
  void Add(const std::string& s) {
    for (int i = 0; i < k_; ++i) bits_[Hash(s, i) % bits_.size()] = true;
  }
  bool MaybeContains(const std::string& s) const {
    for (int i = 0; i < k_; ++i) {
      if (!bits_[Hash(s, i) % bits_.size()]) return false;
    }
    return true;
  }

 private:
  std::vector<bool> bits_;
  int k_;
  static size_t Hash(const std::string& s, int seed) {
    size_t h = 14695981039346656037ull ^ static_cast<size_t>(seed);
    for (unsigned char c : s) {
      h ^= c;
      h *= 1099511628211ull;
    }
    return h;
  }
};

enum class ValueType : uint8_t { kDeletion = 0, kValue = 1 };

// InternalKey: user_key | ~seq (8 bytes, big-endian) | type (1 byte)
std::string EncodeInternalKey(const std::string& user_key, uint64_t seq, ValueType type) {
  std::string out = user_key;
  const uint64_t inv = ~seq;
  for (int i = 7; i >= 0; --i) out.push_back(static_cast<char>((inv >> (i * 8)) & 0xff));
  out.push_back(static_cast<char>(type));
  return out;
}

// MiniKV: MemTable stores InternalKeys; Bloom tracks user keys; latest_ for O(1) read path.
class MiniKV {
 public:
  void Put(const std::string& key, const std::string& value) {
    const uint64_t seq = ++seq_;
    mem_.Put(EncodeInternalKey(key, seq, ValueType::kValue), value);
    bloom_.Add(key);
    latest_[key] = Entry{value, false, seq};
  }

  void Delete(const std::string& key) {
    const uint64_t seq = ++seq_;
    mem_.Put(EncodeInternalKey(key, seq, ValueType::kDeletion), "");
    bloom_.Add(key);
    latest_[key] = Entry{"", true, seq};
  }

  std::optional<std::string> Get(const std::string& key) {
    if (!bloom_.MaybeContains(key)) return std::nullopt;
    auto it = latest_.find(key);
    if (it == latest_.end() || it->second.deleted) return std::nullopt;
    // Cross-check: value must exist in MemTable under the encoded InternalKey.
    auto stored = mem_.Get(EncodeInternalKey(key, it->second.seq, ValueType::kValue));
    assert(stored && *stored == it->second.value);
    return it->second.value;
  }

 private:
  struct Entry {
    std::string value;
    bool deleted;
    uint64_t seq;
  };

  SkipList mem_;
  BloomFilter bloom_{2048, 4};
  uint64_t seq_ = 0;
  std::unordered_map<std::string, Entry> latest_;
};

void RunSmokeTests() {
  MiniKV kv;
  kv.Put("a", "1");
  kv.Put("b", "2");
  assert(kv.Get("a").value() == "1");
  assert(kv.Get("b").value() == "2");
  kv.Put("a", "1b");
  assert(kv.Get("a").value() == "1b");
  kv.Delete("b");
  assert(!kv.Get("b").has_value());
  assert(!kv.Get("missing").has_value());

  SkipList sl;
  sl.Put("k", "v");
  assert(sl.Get("k").value() == "v");

  BloomFilter bl(256, 3);
  bl.Add("hello");
  assert(bl.MaybeContains("hello"));
  assert(!bl.MaybeContains("world"));
}

}  // namespace

int main() {
  SetupConsoleUtf8();

  std::cout << "==== module14_reproduce ====\n";
  std::cout << "TitanKV full reproduce checklist (see docs/course/zh/14-reproduce.md):\n";
  std::cout << "  [ ] WAL / Manifest / VersionSet\n";
  std::cout << "  [ ] MemTable flush -> SSTable\n";
  std::cout << "  [ ] Compaction + MVCC snapshots\n";
  std::cout << "  [ ] RPC / distributed consensus (Raft)\n";
  std::cout << "This smoke test validates: SkipList + BloomFilter + InternalKey Put/Get/Delete\n\n";

  RunSmokeTests();

  std::cout << "[OK] SkipList memtable\n";
  std::cout << "[OK] Bloom filter (no false negatives)\n";
  std::cout << "[OK] InternalKey-style versioned Put/Get/Delete\n";
  std::cout << "ALL CHECKS PASSED — building blocks ready for full TitanKV reproduce\n";
  return 0;
}
