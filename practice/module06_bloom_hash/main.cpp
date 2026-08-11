/*
 * ============================================================================
 * TitanKV 练习 - Module 06: Bloom Filter / Hash / 一致性哈希
 * ============================================================================
 * 目标：
 *   1) MurmurHash2（32-bit）
 *   2) BloomFilter：双重哈希 h1 + i*h2，OptimalBits / OptimalHashes
 *   3) ConsistentHash：带虚拟结点的哈希环
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module06_bloom_hash
 * ============================================================================
 */

#include <cassert>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <map>
#include <string>
#include <vector>

// ---------------------------------------------------------------------------
// MurmurHash2：经典非加密哈希，速度快、分布较好（教学用 32-bit）
// ---------------------------------------------------------------------------
uint32_t MurmurHash2(const void* key, int len, uint32_t seed) {
  const uint32_t m = 0x5bd1e995;
  const int r = 24;
  uint32_t h = seed ^ len;
  const unsigned char* data = static_cast<const unsigned char*>(key);
  while (len >= 4) {
    uint32_t k;
    std::memcpy(&k, data, sizeof(k));
    k *= m;
    k ^= k >> r;
    k *= m;
    h *= m;
    h ^= k;
    data += 4;
    len -= 4;
  }
  switch (len) {
    case 3:
      h ^= data[2] << 16;
      // fallthrough
    case 2:
      h ^= data[1] << 8;
      // fallthrough
    case 1:
      h ^= data[0];
      h *= m;
  }
  h ^= h >> 13;
  h *= m;
  h ^= h >> 15;
  return h;
}

uint32_t HashKey(const std::string& s, uint32_t seed = 0x9747b28c) {
  return MurmurHash2(s.data(), static_cast<int>(s.size()), seed);
}

// ---------------------------------------------------------------------------
// BloomFilter：空间换时间的集合成员近似判断
// ---------------------------------------------------------------------------
// 最优位数 m ≈ -n*ln(p) / (ln2)^2
 // 最优哈希个数 k ≈ (m/n)*ln2
 // 双重哈希：第 i 个哈希 = h1 + i*h2（mod m），避免真正计算 k 次哈希
class BloomFilter {
 public:
  BloomFilter(size_t expected_items, double false_positive_rate)
      : n_(expected_items), p_(false_positive_rate) {
    bits_ = OptimalBits(n_, p_);
    if (bits_ < 64) bits_ = 64;
    k_ = OptimalHashes(bits_, n_);
    if (k_ < 1) k_ = 1;
    storage_.assign((bits_ + 7) / 8, 0);
  }

  static size_t OptimalBits(size_t n, double p) {
    if (n == 0) return 64;
    // m = -n * ln(p) / (ln2)^2
    double m = -static_cast<double>(n) * std::log(p) / (std::log(2.0) * std::log(2.0));
    return static_cast<size_t>(std::ceil(m));
  }

  static size_t OptimalHashes(size_t m, size_t n) {
    if (n == 0) return 1;
    // k = (m/n) * ln2
    double k = (static_cast<double>(m) / static_cast<double>(n)) * std::log(2.0);
    size_t ki = static_cast<size_t>(std::round(k));
    return ki == 0 ? 1 : ki;
  }

  void Add(const std::string& key) {
    uint32_t h1 = HashKey(key, 0x9747b28c);
    uint32_t h2 = HashKey(key, 0x5bd1e995);
    if (h2 == 0) h2 = 0x27d4eb2d;  // 保证步进非 0
    for (size_t i = 0; i < k_; ++i) {
      size_t idx = static_cast<size_t>((h1 + static_cast<uint64_t>(i) * h2) % bits_);
      storage_[idx / 8] |= static_cast<uint8_t>(1u << (idx % 8));
    }
  }

  bool MightContain(const std::string& key) const {
    uint32_t h1 = HashKey(key, 0x9747b28c);
    uint32_t h2 = HashKey(key, 0x5bd1e995);
    if (h2 == 0) h2 = 0x27d4eb2d;
    for (size_t i = 0; i < k_; ++i) {
      size_t idx = static_cast<size_t>((h1 + static_cast<uint64_t>(i) * h2) % bits_);
      if ((storage_[idx / 8] & static_cast<uint8_t>(1u << (idx % 8))) == 0) {
        return false;
      }
    }
    return true;
  }

  size_t bit_size() const { return bits_; }
  size_t hash_count() const { return k_; }

 private:
  size_t n_;
  double p_;
  size_t bits_;
  size_t k_;
  std::vector<uint8_t> storage_;
};

// ---------------------------------------------------------------------------
// ConsistentHash：一致性哈希环 + 虚拟结点
// ---------------------------------------------------------------------------
// 每个物理结点复制 vnode_count 个虚拟结点，均匀撒在环上，降低数据倾斜
class ConsistentHash {
 public:
  explicit ConsistentHash(int vnode_count = 100) : vnode_count_(vnode_count) {}

  void AddNode(const std::string& node) {
    for (int i = 0; i < vnode_count_; ++i) {
      std::string vnode = node + "#" + std::to_string(i);
      uint32_t h = HashKey(vnode);
      ring_[h] = node;
    }
  }

  void RemoveNode(const std::string& node) {
    for (int i = 0; i < vnode_count_; ++i) {
      std::string vnode = node + "#" + std::to_string(i);
      uint32_t h = HashKey(vnode);
      ring_.erase(h);
    }
  }

  // 顺时针找第一个 >= key_hash 的虚拟结点；若无则则回到 begin（环）
  std::string GetNode(const std::string& key) const {
    assert(!ring_.empty());
    uint32_t h = HashKey(key);
    auto it = ring_.lower_bound(h);
    if (it == ring_.end()) it = ring_.begin();
    return it->second;
  }

  size_t ring_size() const { return ring_.size(); }

 private:
  int vnode_count_;
  std::map<uint32_t, std::string> ring_;
};

int main() {
  std::cout << "=== Module06: Bloom / Murmur / ConsistentHash ===\n";

  // Murmur 基本确定性
  {
    uint32_t a = HashKey("titankv");
    uint32_t b = HashKey("titankv");
    assert(a == b);
    assert(HashKey("a") != HashKey("b"));
    std::cout << "[MurmurHash2] 确定性自检通过 hash=" << a << "\n";
  }

  // Bloom：插入 1000 键，测量未插入键的近似假阳性率
  {
    const size_t n = 1000;
    const double target_fp = 0.01;
    BloomFilter bf(n, target_fp);
    std::cout << "[Bloom] m=" << bf.bit_size() << " k=" << bf.hash_count() << "\n";

    for (size_t i = 0; i < n; ++i) {
      bf.Add("key-" + std::to_string(i));
    }
    for (size_t i = 0; i < n; ++i) {
      assert(bf.MightContain("key-" + std::to_string(i)));
    }

    size_t fp = 0;
    const size_t trials = 1000;
    for (size_t i = 0; i < trials; ++i) {
      std::string miss = "miss-" + std::to_string(i);
      if (bf.MightContain(miss)) ++fp;
    }
    double rate = static_cast<double>(fp) / static_cast<double>(trials);
    std::cout << "[Bloom] 粗测假阳性率=" << rate << " (目标约 " << target_fp << ")\n";
    // 粗测允许一定波动，但不应离谱（例如 > 15%）
    assert(rate < 0.15);
  }

  // ConsistentHash
  {
    ConsistentHash ring(50);
    ring.AddNode("nodeA");
    ring.AddNode("nodeB");
    ring.AddNode("nodeC");
    assert(ring.ring_size() == 150);

    std::string n1 = ring.GetNode("user:42");
    assert(n1 == "nodeA" || n1 == "nodeB" || n1 == "nodeC");
    // 同一 key 应稳定映射
    assert(ring.GetNode("user:42") == n1);

    ring.RemoveNode("nodeB");
    assert(ring.ring_size() == 100);
    std::string n2 = ring.GetNode("user:42");
    assert(n2 == "nodeA" || n2 == "nodeC");
    std::cout << "[ConsistentHash] 虚拟结点环自检通过 mapped=" << n1 << "\n";
  }

  std::cout << "module06_bloom_hash SUCCESS\n";
  return 0;
}