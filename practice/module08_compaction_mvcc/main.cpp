// =============================================================================
// Module 08: Compaction & MVCC 练习
// 目标：理解 InternalKey 编码、多版本并发控制(MVCC)快照读、以及分层压缩时的版本回收。
// =============================================================================
#include <algorithm>
#include <cassert>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <map>
#include <optional>
#include <string>
#include <utility>
#include <vector>

// ---------------------------------------------------------------------------
// InternalKey：user_key + sequence(降序) + type
// RocksDB/LevelDB 风格：同一 user_key 下，更大的 sequence 排在前面（更新的版本优先）。
// ---------------------------------------------------------------------------
enum class ValueType : uint8_t { kValue = 1, kDeletion = 0 };

struct InternalKey {
  std::string user_key;
  uint64_t sequence = 0;
  ValueType type = ValueType::kValue;

  // 编码为可比较的字节串：user_key | ~sequence(8B 大端) | type(1B)
  // 对 sequence 取反后按无符号比较，即可实现「sequence 降序」。
  std::string Encode() const {
    std::string out = user_key;
    uint64_t inv = ~sequence;
    char buf[8];
    for (int i = 7; i >= 0; --i) {
      buf[7 - i] = static_cast<char>((inv >> (i * 8)) & 0xff);
    }
    out.append(buf, 8);
    out.push_back(static_cast<char>(type));
    return out;
  }

  static InternalKey Decode(const std::string& enc) {
    assert(enc.size() >= 9);
    InternalKey ik;
    ik.user_key = enc.substr(0, enc.size() - 9);
    uint64_t inv = 0;
    for (int i = 0; i < 8; ++i) {
      inv = (inv << 8) | static_cast<uint8_t>(enc[enc.size() - 9 + i]);
    }
    ik.sequence = ~inv;
    ik.type = static_cast<ValueType>(static_cast<uint8_t>(enc.back()));
    return ik;
  }

  // 比较规则：先比 user_key；相同则 sequence 大的更小（排在前）；再比 type。
  static int Compare(const std::string& a, const std::string& b) {
    InternalKey ka = Decode(a);
    InternalKey kb = Decode(b);
    if (ka.user_key < kb.user_key) return -1;
    if (ka.user_key > kb.user_key) return 1;
    if (ka.sequence > kb.sequence) return -1;  // 大 seq 在前
    if (ka.sequence < kb.sequence) return 1;
    if (static_cast<uint8_t>(ka.type) < static_cast<uint8_t>(kb.type)) return -1;
    if (static_cast<uint8_t>(ka.type) > static_cast<uint8_t>(kb.type)) return 1;
    return 0;
  }
};

// ---------------------------------------------------------------------------
// MVCC Snapshot：每个 key 维护版本链 {seq, value, deleted}
// Get(key, snapshot_seq)：返回 sequence <= snapshot_seq 的最新可见版本。
// ---------------------------------------------------------------------------
struct Version {
  uint64_t seq;
  std::string value;
  bool deleted;
};

class MvccStore {
 public:
  void Put(const std::string& key, uint64_t seq, const std::string& value) {
    versions_[key].push_back(Version{seq, value, false});
  }
  void Delete(const std::string& key, uint64_t seq) {
    versions_[key].push_back(Version{seq, "", true});
  }

  // 快照读：只看见 seq <= snapshot_seq 的写入
  std::optional<std::string> Get(const std::string& key, uint64_t snapshot_seq) const {
    auto it = versions_.find(key);
    if (it == versions_.end()) return std::nullopt;
    const Version* best = nullptr;
    for (const auto& v : it->second) {
      if (v.seq <= snapshot_seq) {
        if (!best || v.seq > best->seq) best = &v;
      }
    }
    if (!best || best->deleted) return std::nullopt;
    return best->value;
  }

  const std::map<std::string, std::vector<Version>>& data() const { return versions_; }

 private:
  std::map<std::string, std::vector<Version>> versions_;
};

// ---------------------------------------------------------------------------
// 简易 leveled compaction：合并两个已按 InternalKey 排序的 run，
// 丢弃低于 min_snapshot 的过期版本 / 可回收 tombstone。
// ---------------------------------------------------------------------------
struct KvEntry {
  std::string user_key;
  uint64_t seq;
  ValueType type;
  std::string value;

  std::string EncodeKey() const {
    InternalKey ik{user_key, seq, type};
    return ik.Encode();
  }
};

// 输入 runs 已按 InternalKey 序排好；输出合并后仍有序的 run。
std::vector<KvEntry> CompactMerge(const std::vector<KvEntry>& a,
                                  const std::vector<KvEntry>& b,
                                  uint64_t min_snapshot) {
  // 先按 InternalKey 归并
  std::vector<KvEntry> merged;
  size_t i = 0, j = 0;
  auto less = [](const KvEntry& x, const KvEntry& y) {
    return InternalKey::Compare(x.EncodeKey(), y.EncodeKey()) < 0;
  };
  while (i < a.size() || j < b.size()) {
    if (j >= b.size() || (i < a.size() && less(a[i], b[j]))) {
      merged.push_back(a[i++]);
    } else {
      merged.push_back(b[j++]);
    }
  }

  // 按 user_key 分组，保留：所有 seq >= min_snapshot 的版本；
  // 以及每个 key 在 min_snapshot 之下「最新」的一个版本（若存在）。
  // 若该「最新」是 tombstone 且没有更高 seq 需要保留，则可整键丢弃（无快照再看到）。
  std::vector<KvEntry> out;
  size_t n = merged.size();
  size_t idx = 0;
  while (idx < n) {
    size_t start = idx;
    const std::string& uk = merged[idx].user_key;
    while (idx < n && merged[idx].user_key == uk) ++idx;
    // [start, idx) 同一 user_key，已按 seq 降序（InternalKey 序）
    std::vector<KvEntry> keep;
    bool kept_below = false;
    for (size_t k = start; k < idx; ++k) {
      if (merged[k].seq >= min_snapshot) {
        keep.push_back(merged[k]);
      } else if (!kept_below) {
        // 只保留低于快照线的最新一条，供旧快照读取
        keep.push_back(merged[k]);
        kept_below = true;
      }
      // 更旧的版本全部丢弃
    }
    // 若最终只剩一条 tombstone 且 seq < min_snapshot，则可安全删除整键
    if (keep.size() == 1 && keep[0].type == ValueType::kDeletion &&
        keep[0].seq < min_snapshot) {
      // drop
    } else {
      for (auto& e : keep) out.push_back(std::move(e));
    }
  }
  return out;
}

int main() {
  std::cout << "==== module08_compaction_mvcc ====\n";

  // ---- InternalKey 编解码与比较 ----
  InternalKey k1{"apple", 10, ValueType::kValue};
  InternalKey k2{"apple", 5, ValueType::kValue};
  InternalKey k3{"banana", 1, ValueType::kDeletion};
  auto e1 = k1.Encode();
  auto e2 = k2.Encode();
  auto e3 = k3.Encode();
  assert(InternalKey::Compare(e1, e2) < 0);  // seq10 在 seq5 前
  assert(InternalKey::Compare(e1, e3) < 0);  // apple < banana
  auto d1 = InternalKey::Decode(e1);
  assert(d1.user_key == "apple" && d1.sequence == 10 && d1.type == ValueType::kValue);
  std::cout << "[OK] InternalKey encode/compare/decode\n";

  // ---- MVCC 快照读 ----
  MvccStore store;
  store.Put("x", 1, "v1");
  store.Put("x", 3, "v3");
  store.Delete("x", 5);
  store.Put("x", 7, "v7");
  assert(store.Get("x", 2).value() == "v1");
  assert(store.Get("x", 4).value() == "v3");
  assert(!store.Get("x", 6).has_value());  // 被 seq5 删除
  assert(store.Get("x", 8).value() == "v7");
  std::cout << "[OK] MVCC snapshot Get\n";

  // ---- 压缩合并：丢弃过期版本 ----
  // run A / B 中 apple 有多版本；min_snapshot=4 时，seq<4 的旧版本只留最新一条
  std::vector<KvEntry> runA = {
      {"apple", 10, ValueType::kValue, "a10"},
      {"apple", 2, ValueType::kValue, "a2"},
      {"zebra", 1, ValueType::kValue, "z1"},
  };
  std::vector<KvEntry> runB = {
      {"apple", 8, ValueType::kValue, "a8"},
      {"apple", 1, ValueType::kDeletion, ""},
      {"mango", 3, ValueType::kValue, "m3"},
  };
  // 保证输入有序
  auto byIk = [](const KvEntry& x, const KvEntry& y) {
    return InternalKey::Compare(x.EncodeKey(), y.EncodeKey()) < 0;
  };
  std::sort(runA.begin(), runA.end(), byIk);
  std::sort(runB.begin(), runB.end(), byIk);

  auto compacted = CompactMerge(runA, runB, /*min_snapshot=*/4);
  // 期望：apple 保留 seq 10,8 以及低于 4 的最新(seq2)；mango seq3；zebra seq1
  // apple seq1 deletion 应被丢弃（被 seq2 覆盖且都 < 4）
  int apple_count = 0;
  bool has_seq1_del = false;
  for (const auto& e : compacted) {
    if (e.user_key == "apple") {
      ++apple_count;
      if (e.seq == 1 && e.type == ValueType::kDeletion) has_seq1_del = true;
    }
  }
  assert(!has_seq1_del);
  assert(apple_count == 3);  // 10, 8, 2
  std::cout << "[OK] compaction drops obsolete versions, size=" << compacted.size() << "\n";

  // tombstone 回收：仅 tombstone 且低于 min_snapshot
  std::vector<KvEntry> tA = {{"gone", 1, ValueType::kDeletion, ""}};
  std::vector<KvEntry> tB;
  auto tOut = CompactMerge(tA, tB, 10);
  assert(tOut.empty());
  std::cout << "[OK] tombstone below min_snapshot dropped\n";

  std::cout << "ALL CHECKS PASSED\n";
  return 0;
}