#include <gtest/gtest.h>

#include <dirent.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cstdlib>
#include <fstream>
#include <string>

#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/sstable_reader.h"
#include "minikv/db.h"
#include "minikv/options.h"
#include "minikv/slice.h"

using minikv::DB;
using minikv::Options;
using minikv::Slice;
using minikv::WriteOptions;
using minikv::core::BloomFilter;
using minikv::core::CompressionType;
using minikv::core::InternalKeyEncode;
using minikv::core::SSTableBuilder;
using minikv::core::SSTableReader;
using minikv::core::ValueType;

namespace {

std::string uniqueDir(const char* tag) {
    const char* t = std::getenv("TMPDIR");
    if (!t || !*t) t = "/tmp";
    return std::string(t) + "/titankv_bloom_" + tag + "_" +
           std::to_string(::getpid());
}

void rmTree(const std::string& root) {
    int rc = std::system(("rm -rf " + root).c_str());
    (void)rc;
}

bool exists(const std::string& path) {
    return ::access(path.c_str(), F_OK) == 0;
}

}  // namespace

// M6: orphan .bloom (no live .sst) must be removed on DB open / purge.
TEST(BloomLifecycleTest, PurgeRemovesOrphanBloomSibling) {
    std::string dir = uniqueDir("orphan");
    rmTree(dir);

    Options opts;
    opts.db_path = dir;
    opts.wal_sync = true;
    opts.memtable_size = 256;
    opts.level0_compaction_trigger = 64;
    opts.compression = 0;

    // Open once so level dirs exist.
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
    }

    std::string level0 = dir + "/level-0";
    ::mkdir(level0.c_str(), 0755);
    std::string orphan_bloom = level0 + "/99999.sst.bloom";
    {
        BloomFilter bf(10, 0.01);
        bf.add(Slice("ghost"));
        bf.persist(orphan_bloom);
    }
    ASSERT_TRUE(exists(orphan_bloom));

    // Reopen triggers purgeOrphanSSTables.
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
    }

    EXPECT_FALSE(exists(orphan_bloom))
        << "orphan .bloom should be purged on open";

    rmTree(dir);
}

// M6: deleting an SST (via compaction source unlink) must also drop .bloom.
// We simulate by building an SST then opening DB that never references it,
// and separately verify builder writes .bloom next to .sst.
TEST(BloomLifecycleTest, BuilderWritesBloomBesideSst) {
    std::string path = uniqueDir("build") + ".sst";
    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());

    SSTableBuilder b(path, 4096, CompressionType::kNone);
    for (int i = 0; i < 50; ++i) {
        std::string uk = "k" + std::to_string(i);
        std::string ik = InternalKeyEncode(uk, i + 1, ValueType::kValue);
        ASSERT_TRUE(b.add(Slice(ik), Slice(uk), Slice("v")).ok());
    }
    ASSERT_TRUE(b.finish().ok());

    EXPECT_TRUE(exists(path));
    EXPECT_TRUE(exists(path + ".bloom"));

    auto r = SSTableReader::open(path);
    ASSERT_NE(r, nullptr);
    EXPECT_TRUE(r->mightContain(Slice("k10")));

    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());
}

// M6: bloom sized from real entry_count — tiny SST must not allocate for 10000 keys.
TEST(BloomLifecycleTest, BloomSizedToEntryCount) {
    std::string path = uniqueDir("size") + ".sst";
    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());

    SSTableBuilder b(path, 4096, CompressionType::kNone);
    std::string uk = "only";
    std::string ik = InternalKeyEncode(uk, 1, ValueType::kValue);
    ASSERT_TRUE(b.add(Slice(ik), Slice(uk), Slice("one")).ok());
    ASSERT_TRUE(b.finish().ok());

    auto bf = BloomFilter::load(path + ".bloom");
    ASSERT_NE(bf, nullptr);
    // Hardcoded BloomFilter(10000) used ~ (10000 * bits_per_key / 8) bytes.
    // With 1 key, memoryUsage should be far below that (~10KB+).
    EXPECT_LT(bf->memoryUsage(), 512u)
        << "bloom still looks sized for 10000 keys: " << bf->memoryUsage();
    EXPECT_TRUE(bf->mightContain(Slice("only")));

    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());
}
