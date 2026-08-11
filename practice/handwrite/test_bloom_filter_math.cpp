// Module 06：Bloom 误判率公式验证
// 公式：
//   m = -n * ln(p) / (ln2)^2     （所需比特数）
//   k = (m / n) * ln2            （最优哈希函数数）
// 测试：n=1e6, p=1%，断言计算结果与预期接近
//
// 本文件为独立实现，不依赖 minikv 源码。

#include <gtest/gtest.h>

#include <cmath>

namespace {

// 计算 Bloom filter 最优参数。
struct BloomParams {
    double m;  // 比特数（理论值，浮点）
    double k;  // 哈希函数数（理论值，浮点）
};

BloomParams compute_bloom_params(double n, double p) {
    const double ln2 = std::log(2.0);
    const double m = -n * std::log(p) / (ln2 * ln2);
    const double k = (m / n) * ln2;
    return {m, k};
}

}  // namespace

// ===== 单元测试 =====

TEST(BloomFilterMathTest, n1e6_p1Percent_MatchesExpected) {
    const double n = 1'000'000.0;
    const double p = 0.01;
    auto bp = compute_bloom_params(n, p);

    // 期望值（手工推导）：
    //   ln(0.01)        = -4.605170186...
    //   (ln2)^2         = 0.480453014...
    //   m = 1e6 * 4.605170186 / 0.480453014 = 9585058.377...
    //   k = (9585058.377/1e6) * 0.693147181 = 6.6438562...
    const double expected_m = 9585058.377;
    const double expected_k = 6.6438562;

    EXPECT_NEAR(bp.m, expected_m, 1.0);    // 允许 1 bit 误差
    EXPECT_NEAR(bp.k, expected_k, 1e-4);

    // 验证最优 k 与"经典公式 k = (ln2) * (m/n)"一致。
    const double ln2 = std::log(2.0);
    EXPECT_NEAR(bp.k, ln2 * (bp.m / n), 1e-6);
}

TEST(BloomFilterMathTest, kIsAboutSevenForOnePercentFPR) {
    // p=1% 时最优 k ≈ 6.64，工程上取上取整为 7 个哈希函数。
    const double n = 1'000'000.0;
    const double p = 0.01;
    auto bp = compute_bloom_params(n, p);
    EXPECT_GE(bp.k, 6.0);
    EXPECT_LE(bp.k, 7.0);
    // 工程上常用 ceil(k) = 7
    EXPECT_EQ(static_cast<int>(std::ceil(bp.k)), 7);
}

TEST(BloomFilterMathTest, MemoryFootprintAboutOneMB) {
    // n=1e6, p=1% 时所需比特数约 9.585M bits ≈ 1.198 MBytes。
    const double n = 1'000'000.0;
    const double p = 0.01;
    auto bp = compute_bloom_params(n, p);
    const double bytes = bp.m / 8.0;
    const double mib = bytes / (1024.0 * 1024.0);
    EXPECT_NEAR(mib, 1.140, 0.05);
    // 9585058.38 bits / 8 = 1198132.30 bytes = 1.1424 MiB
}

TEST(BloomFilterMathTest, FalsePositiveRateScalesWithBits) {
    // 固定 n，p 越小 m 越大。
    const double n = 1'000'000.0;
    auto a = compute_bloom_params(n, 0.01);   // 1%
    auto b = compute_bloom_params(n, 0.001);  // 0.1%
    EXPECT_GT(b.m, a.m);
    EXPECT_GT(b.k, a.k);
}

TEST(BloomFilterMathTest, AchievableFPREqualsTarget) {
    // 反算：给定 m, k, n，理论 FPR = (1 - e^(-k*n/m))^k
    // 验证代入最优 m/k 后，反算 FPR 接近目标 p。
    const double n = 1'000'000.0;
    const double p = 0.01;
    auto bp = compute_bloom_params(n, p);
    const double ratio = (bp.k * n) / bp.m;
    const double fpr = std::pow(1.0 - std::exp(-ratio), bp.k);
    EXPECT_NEAR(fpr, p, 1e-3);
}
