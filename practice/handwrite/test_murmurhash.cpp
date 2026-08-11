// Module 06：MurmurHash2 实现 + 分布均匀性测试
// 参考：https://github.com/aappleby/smhasher/blob/master/src/MurmurHash2.cpp
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <cstdint>
#include <cstring>
#include <random>
#include <string>
#include <unordered_map>
#include <vector>

namespace {

// MurmurHash2 (32-bit)，Austin Appleby 的原始实现。
// 注意：该哈希不是加密安全的，但分布均匀、碰撞率低，适合 Bloom filter / 哈希表。
inline uint32_t murmurHash2(const void* key, size_t len, uint32_t seed) {
    const uint32_t m = 0x5bd1e995;
    const int r = 24;
    uint32_t h = seed ^ static_cast<uint32_t>(len);
    const auto* data = static_cast<const unsigned char*>(key);

    while (len >= 4) {
        uint32_t k = static_cast<uint32_t>(data[0]) |
                     (static_cast<uint32_t>(data[1]) << 8) |
                     (static_cast<uint32_t>(data[2]) << 16) |
                     (static_cast<uint32_t>(data[3]) << 24);
        k *= m;
        k ^= k >> r;
        k *= m;
        h *= m;
        h ^= k;
        data += 4;
        len -= 4;
    }

    switch (len) {
        case 3: h ^= static_cast<uint32_t>(data[2]) << 16; [[fallthrough]];
        case 2: h ^= static_cast<uint32_t>(data[1]) << 8;  [[fallthrough]];
        case 1: h ^= static_cast<uint32_t>(data[0]);
            h *= m;
    }

    h ^= h >> 13;
    h *= m;
    h ^= h >> 15;
    return h;
}

inline uint32_t murmurHash2(const std::string& s, uint32_t seed) {
    return murmurHash2(s.data(), s.size(), seed);
}

}  // namespace

// ===== 单元测试 =====

TEST(MurmurHashTest, KnownVectorValues) {
    // 空 seed=0 → 0
    EXPECT_EQ(murmurHash2("", 0), 0u);
    // 不同输入应产生不同输出（非雪崩，但基本不同）
    EXPECT_NE(murmurHash2("hello", 0), murmurHash2("world", 0));
    // 相同输入相同 seed 应稳定
    EXPECT_EQ(murmurHash2("TitanKV", 42), murmurHash2("TitanKV", 42));
    // 不同 seed 产生不同哈希
    EXPECT_NE(murmurHash2("TitanKV", 1), murmurHash2("TitanKV", 2));
}

TEST(MurmurHashTest, AvalancheOnSingleBitFlip) {
    // 单比特翻转应使结果大幅变化（雪崩特性）
    std::string base = "abcdefghij";
    uint32_t h0 = murmurHash2(base, 0);
    for (size_t i = 0; i < base.size(); ++i) {
        std::string copy = base;
        copy[i] = static_cast<char>(static_cast<unsigned char>(copy[i]) ^ 0x01);
        uint32_t h = murmurHash2(copy, 0);
        EXPECT_NE(h, h0);
    }
}

TEST(MurmurHashTest, DeterministicAcrossCalls) {
    std::mt19937 rng(12345);
    for (int i = 0; i < 100; ++i) {
        std::string s(16, '\0');
        for (auto& c : s) c = static_cast<char>(rng());
        uint32_t a = murmurHash2(s, 0xdeadbeef);
        uint32_t b = murmurHash2(s, 0xdeadbeef);
        EXPECT_EQ(a, b);
    }
}

TEST(MurmurHashTest, UniformDistributionInBuckets) {
    // 将 1,000,000 个不同 key 哈希到 1024 个桶，
    // 验证每个桶的计数与期望 (N/B) 偏差在合理范围内（卡方检验风格）。
    constexpr size_t N = 1'000'000;
    constexpr uint32_t B = 1024;
    constexpr double expected = static_cast<double>(N) / B;  // 976.5625

    std::vector<uint64_t> counts(B, 0);
    std::mt19937_64 rng(2024);
    for (size_t i = 0; i < N; ++i) {
        // 用 8 字节整数作为 key
        uint64_t key = rng();
        uint32_t h = murmurHash2(&key, sizeof(key), 0);
        ++counts[h % B];
    }

    // 计算卡方统计量：sum((obs - exp)^2 / exp)
    double chi2 = 0.0;
    uint64_t min_c = counts[0], max_c = counts[0];
    for (auto c : counts) {
        if (c < min_c) min_c = c;
        if (c > max_c) max_c = c;
        double diff = static_cast<double>(c) - expected;
        chi2 += diff * diff / expected;
    }
    // 自由度 B-1=1023，卡方均值约等于自由度，标准差 sqrt(2*df) ≈ 45.3。
    // 上 0.001 分位数约 1175。我们用宽松上界 1500 判定均匀。
    EXPECT_LT(chi2, 1500.0);

    // 每个桶计数应在期望值 ±20% 内（MurmurHash2 分布良好，留足安全余量）。
    for (auto c : counts) {
        EXPECT_GT(c, static_cast<uint64_t>(expected * 0.80));
        EXPECT_LT(c, static_cast<uint64_t>(expected * 1.20));
    }
    // 极差不应过大
    EXPECT_LT(max_c - min_c, static_cast<uint64_t>(expected * 0.4));
}

TEST(MurmurHashTest, StringKeyDistribution) {
    // 字符串 key 的分布均匀性。
    constexpr size_t N = 500'000;
    constexpr uint32_t B = 256;
    constexpr double expected = static_cast<double>(N) / B;  // 1953.125

    std::vector<uint64_t> counts(B, 0);
    for (size_t i = 0; i < N; ++i) {
        std::string key = "key_" + std::to_string(i) + "_" + std::to_string(i * 7);
        uint32_t h = murmurHash2(key, 0);
        ++counts[h % B];
    }
    for (auto c : counts) {
        EXPECT_GT(c, static_cast<uint64_t>(expected * 0.85));
        EXPECT_LT(c, static_cast<uint64_t>(expected * 1.15));
    }
}

TEST(MurmurHashTest, DoubleHashingProducesIndependentPair) {
    // Bloom filter 中常用 h1 + i*h2 模式：h2 不应是 h1 的简单倍数。
    constexpr size_t N = 100'000;
    std::mt19937 rng(7777);
    int collisions = 0;
    for (size_t i = 0; i < N; ++i) {
        uint64_t key = rng();
        uint32_t h1 = murmurHash2(&key, sizeof(key), 0xbc9f1d34);
        uint32_t h2 = murmurHash2(&key, sizeof(key), 0x9e3779b9);
        // h2 不应为 0（避免 i*h2=0 退化），且不应等于 h1
        if (h2 == 0 || h1 == h2) ++collisions;
    }
    // 两个独立种子的 MurmurHash2 输出碰撞概率约 1/2^32，10 万 key 下期望
    // 碰撞数约 2e-5。允许极小余量以避免边界确定性失败。
    EXPECT_LE(collisions, 1);
}
