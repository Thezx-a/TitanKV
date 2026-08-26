#include <gtest/gtest.h>

#include <sys/stat.h>
#include <unistd.h>

#include <memory>
#include <string>

#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/table_cache.h"
#include "minikv/options.h"
#include "minikv/slice.h"

using namespace minikv;
using namespace minikv::core;

namespace {

std::string tmpDir(const char* tag) {
    std::string d = std::string("/tmp/titankv_tcache_") + tag + "_" +
                    std::to_string(::getpid());
    ::mkdir(d.c_str(), 0755);
    return d;
}

std::string buildOneSst(const std::string& dir, int id) {
    std::string path = dir + "/" + std::to_string(id) + ".sst";
    SSTableBuilder b(path, 4096, CompressionType::kNone);
    std::string ik = InternalKeyEncode(Slice("k"), 1, ValueType::kValue);
    b.add(Slice(ik), Slice("k"), Slice("v"));
    EXPECT_TRUE(b.finish().ok());
    return path;
}

}  // namespace

TEST(TableCacheTest, HitReusesSameReader) {
    std::string dir = tmpDir("hit");
    std::string path = buildOneSst(dir, 1);

    TableCache cache(8, nullptr);
    auto a = cache.get(path);
    auto b = cache.get(path);
    ASSERT_NE(a, nullptr);
    ASSERT_NE(b, nullptr);
    EXPECT_EQ(a.get(), b.get());
    EXPECT_EQ(cache.size(), 1u);

    std::string out;
    PointLookup pl;
    ASSERT_TRUE(a->lookup(Slice("k"), &out, &pl).ok());
    EXPECT_EQ(pl, PointLookup::kValue);
    EXPECT_EQ(out, "v");

    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());
    ::rmdir(dir.c_str());
}

TEST(TableCacheTest, EvictDropsEntry) {
    std::string dir = tmpDir("evict");
    std::string path = buildOneSst(dir, 2);

    TableCache cache(8, nullptr);
    ASSERT_NE(cache.get(path), nullptr);
    EXPECT_EQ(cache.size(), 1u);
    cache.evict(path);
    EXPECT_EQ(cache.size(), 0u);

    auto again = cache.get(path);
    ASSERT_NE(again, nullptr);
    EXPECT_EQ(cache.size(), 1u);

    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());
    ::rmdir(dir.c_str());
}

TEST(TableCacheTest, CapacityEvictsOldest) {
    std::string dir = tmpDir("cap");
    TableCache cache(2, nullptr);
    std::string p1 = buildOneSst(dir, 1);
    std::string p2 = buildOneSst(dir, 2);
    std::string p3 = buildOneSst(dir, 3);

    ASSERT_NE(cache.get(p1), nullptr);
    ASSERT_NE(cache.get(p2), nullptr);
    ASSERT_NE(cache.get(p3), nullptr);
    EXPECT_LE(cache.size(), 2u);

    auto r1 = cache.get(p1);
    ASSERT_NE(r1, nullptr);
    EXPECT_LE(cache.size(), 2u);

    for (const auto& p : {p1, p2, p3}) {
        ::unlink(p.c_str());
        ::unlink((p + ".bloom").c_str());
    }
    ::rmdir(dir.c_str());
}
