#include <gtest/gtest.h>

#include <atomic>
#include <cstdlib>
#include <string>
#include <thread>
#include <chrono>

#include "minikv/db.h"
#include "minikv/options.h"

using minikv::DB;
using minikv::Options;
using minikv::ReadOptions;
using minikv::Status;
using minikv::WriteOptions;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    uint64_t n = counter.fetch_add(1);
    return std::string(t) + "/titankv_compact_tomb_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

void rmTree(const std::string& root) {
    std::string cmd = "rm -rf " + root;
    int rc = std::system(cmd.c_str());
    (void)rc;
}

// Force memtable flush with one logical key by padding junk keys.
void flushWithKey(DB* db, const std::string& key, const std::string& value,
                  bool is_delete) {
    WriteOptions wo;
    wo.sync = true;
    if (is_delete) {
        ASSERT_TRUE(db->del(wo, key).ok());
    } else {
        ASSERT_TRUE(db->put(wo, key, value).ok());
    }
    // Pad until memtable flushes (memtable_size is tiny in these tests).
    for (int i = 0; i < 80; ++i) {
        std::string pk = "__pad_" + key + "_" + std::to_string(i);
        std::string pv = "pad-value-xxxxxxxxxxxxxxxx" + std::to_string(i);
        ASSERT_TRUE(db->put(wo, pk, pv).ok());
    }
    db->waitFlush();
}

void compactAndWait(DB* db) {
    db->compact();
    db->waitCompaction();
}

}  // namespace

// [P0-1] Deletion must survive L0→L1 compaction when an older value already
// lives in L1. Pre-fix: mergeLevelFiles dropped every tombstone, so Get
// resurrected the L1 value.
TEST(CompactionTombstoneTest, DeleteSurvivesL0ToL1WhenOlderValueInL1) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;
    opts.wal_sync = true;
    opts.level0_compaction_trigger = 64;  // manual compact only
    opts.max_level = 7;

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());

        flushWithKey(db.get(), "k", "v1-alive", /*is_delete=*/false);
        compactAndWait(db.get());  // L0 → L1, value now in L1

        flushWithKey(db.get(), "k", "", /*is_delete=*/true);
        compactAndWait(db.get());  // L0 tombstone → L1; must not resurrect

        std::string got;
        Status s = db->get(ReadOptions{}, "k", &got);
        EXPECT_TRUE(s.isNotFound())
            << "deletion resurrected after L0→L1: " << s.message()
            << " value=" << got;
    }

    // Restart must still see NotFound (tombstone persisted, not dropped).
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        Status s = db->get(ReadOptions{}, "k", &got);
        EXPECT_TRUE(s.isNotFound())
            << "deletion lost across reopen: " << s.message()
            << " value=" << got;
    }

    rmTree(dir);
}

// Memtable tombstone must hide older SST values (Get 三态；与压缩无关的读路径).
TEST(CompactionTombstoneTest, MemTableTombstoneHidesOlderSstValue) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;
    opts.wal_sync = true;
    opts.level0_compaction_trigger = 64;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());

    flushWithKey(db.get(), "k", "old", /*is_delete=*/false);
    compactAndWait(db.get());

    WriteOptions wo;
    wo.sync = true;
    ASSERT_TRUE(db->del(wo, "k").ok());  // tombstone still in memtable

    std::string got;
    Status s = db->get(ReadOptions{}, "k", &got);
    EXPECT_TRUE(s.isNotFound())
        << "memtable tombstone failed to hide SST value: " << got;

    rmTree(dir);
}
