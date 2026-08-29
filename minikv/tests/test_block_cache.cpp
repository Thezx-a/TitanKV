#include <gtest/gtest.h>
#include "core/block_cache.h"
#include "core/sstable_builder.h"
#include "core/sstable_reader.h"
#include "core/internal_key.h"
#include "minikv/options.h"
#include "utils/metrics.h"
#include <cstdio>
#include <string>
#include <unistd.h>

namespace minikv {
namespace core {
namespace {

TEST(BlockCacheTest, ReadBlockHitsCache) {
    std::string path = "/tmp/minikv_block_cache_test.sst";
    std::remove(path.c_str());

    SSTableBuilder builder(path, 4096);
    std::string ik = InternalKeyEncode(Slice("user"), 1, ValueType::kValue);
    ASSERT_TRUE(builder.add(Slice(ik), Slice("user"), Slice("hello")).ok());
    ASSERT_TRUE(builder.finish().ok());

    BlockCache cache(1024 * 1024);
    auto reader = SSTableReader::open(path, &cache);
    ASSERT_NE(reader, nullptr);

    auto v1 = reader->get(Slice("user"));
    ASSERT_TRUE(v1.has_value());
    EXPECT_EQ(*v1, "hello");
    EXPECT_GE(cache.entryCount(), 1u);

    auto v2 = reader->get(Slice("user"));
    ASSERT_TRUE(v2.has_value());
    EXPECT_EQ(*v2, "hello");

    std::remove(path.c_str());
    std::remove((path + ".bloom").c_str());
}

// E3: BlockCache get/miss must bump EngineMetrics (interview-visible /metrics).
TEST(BlockCacheTest, MetricsBumpOnHitAndMiss) {
    std::string path = "/tmp/minikv_block_cache_metrics.sst";
    std::remove(path.c_str());

    SSTableBuilder builder(path, 4096);
    std::string ik = InternalKeyEncode(Slice("k1"), 1, ValueType::kValue);
    ASSERT_TRUE(builder.add(Slice(ik), Slice("k1"), Slice("v1")).ok());
    ASSERT_TRUE(builder.finish().ok());

    auto& m = utils::EngineMetrics::instance();
    const uint64_t hits0 = m.block_cache_hits.load();
    const uint64_t misses0 = m.block_cache_misses.load();

    BlockCache cache(1024 * 1024);
    auto reader = SSTableReader::open(path, &cache);
    ASSERT_NE(reader, nullptr);

    // First get: cache miss on data block → fill cache.
    auto v1 = reader->get(Slice("k1"));
    ASSERT_TRUE(v1.has_value());
    EXPECT_GE(m.block_cache_misses.load(), misses0 + 1);

    // Second get: same block should hit.
    auto v2 = reader->get(Slice("k1"));
    ASSERT_TRUE(v2.has_value());
    EXPECT_GE(m.block_cache_hits.load(), hits0 + 1);

    std::string text = m.prometheusText();
    EXPECT_NE(text.find("titankv_engine_block_cache_hits_total"), std::string::npos);
    EXPECT_NE(text.find("titankv_engine_block_cache_misses_total"), std::string::npos);

    std::remove(path.c_str());
    std::remove((path + ".bloom").c_str());
}

}  // namespace
}  // namespace core
}  // namespace minikv
