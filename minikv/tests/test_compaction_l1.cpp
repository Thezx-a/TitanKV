#include <gtest/gtest.h>

#include <atomic>
#include <cstdlib>
#include <dirent.h>
#include <string>
#include <unistd.h>

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
    return std::string(t) + "/titankv_compact_l1_" +
           std::to_string(::getpid()) + "_" +
           std::to_string(counter.fetch_add(1));
}

void rmTree(const std::string& root) {
    int rc = std::system(("rm -rf " + root).c_str());
    (void)rc;
}

int countSst(const std::string& levelDir) {
    DIR* d = ::opendir(levelDir.c_str());
    if (!d) return 0;
    int n = 0;
    while (dirent* e = ::readdir(d)) {
        std::string name = e->d_name;
        if (name.size() > 4 && name.substr(name.size() - 4) == ".sst") ++n;
    }
    ::closedir(d);
    return n;
}

void flushWithKey(DB* db, const std::string& key, const std::string& value,
                  bool is_delete) {
    WriteOptions wo;
    wo.sync = true;
    if (is_delete) {
        ASSERT_TRUE(db->del(wo, key).ok());
    } else {
        ASSERT_TRUE(db->put(wo, key, value).ok());
    }
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

// E1: compactLevel(L1→L2) must actually merge (≥2 L1 files → L2 SST),
// not return Ok() as a stub. Keys remain readable after the merge.
TEST(CompactionL1Test, L1ToL2MergesAndKeepsValues) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;
    opts.wal_sync = true;
    opts.level0_compaction_trigger = 64;  // manual L0 compact
    opts.max_level = 7;

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());

        flushWithKey(db.get(), "a", "va", /*is_delete=*/false);
        compactAndWait(db.get());  // L0 → L1 file #1

        flushWithKey(db.get(), "b", "vb", /*is_delete=*/false);
        compactAndWait(db.get());  // L0 → L1 file #2; then L1→L2 when ≥2

        EXPECT_GE(countSst(dir + "/level-2"), 1)
            << "L1→L2 must produce at least one SST (compactLevel not stub)";
        EXPECT_EQ(countSst(dir + "/level-1"), 0)
            << "L1 inputs should be removed after merge into L2";

        std::string got;
        ASSERT_TRUE(db->get(ReadOptions{}, "a", &got).ok());
        EXPECT_EQ(got, "va");
        ASSERT_TRUE(db->get(ReadOptions{}, "b", &got).ok());
        EXPECT_EQ(got, "vb");
    }

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        ASSERT_TRUE(db->get(ReadOptions{}, "a", &got).ok());
        EXPECT_EQ(got, "va");
        ASSERT_TRUE(db->get(ReadOptions{}, "b", &got).ok());
        EXPECT_EQ(got, "vb");
    }

    rmTree(dir);
}

// E1 / M1 deeper slice: tombstone must survive L0→L1→L2 + reopen
// (industrialization-plan M1 acceptance: L1→L2, not only L0→L1).
TEST(CompactionL1Test, DeleteSurvivesL1ToL2AndReopen) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;
    opts.wal_sync = true;
    opts.level0_compaction_trigger = 64;
    opts.max_level = 7;  // L2 is not bottom → tombstone must be retained

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());

        flushWithKey(db.get(), "k", "v1-alive", /*is_delete=*/false);
        compactAndWait(db.get());  // value → L1

        flushWithKey(db.get(), "k", "", /*is_delete=*/true);
        compactAndWait(db.get());  // tombstone → L1; ≥2 L1 → L2

        EXPECT_GE(countSst(dir + "/level-2"), 1);

        std::string got;
        Status s = db->get(ReadOptions{}, "k", &got);
        EXPECT_TRUE(s.isNotFound())
            << "deletion resurrected after L1→L2: " << s.message()
            << " value=" << got;
    }

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        Status s = db->get(ReadOptions{}, "k", &got);
        EXPECT_TRUE(s.isNotFound())
            << "deletion lost across reopen after L1→L2: " << s.message()
            << " value=" << got;
    }

    rmTree(dir);
}
