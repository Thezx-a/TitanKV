#include <gtest/gtest.h>

#include <atomic>
#include <cstdlib>
#include <string>
#include <unistd.h>

#include "minikv/db.h"
#include "minikv/options.h"

using minikv::DB;
using minikv::Options;
using minikv::ReadOptions;
using minikv::WriteOptions;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    return std::string(t) + "/titankv_cstream_" + std::to_string(::getpid()) +
           "_" + std::to_string(counter.fetch_add(1));
}

void rmTree(const std::string& root) {
    int rc = std::system(("rm -rf " + root).c_str());
    (void)rc;
}

}  // namespace

// T2.1: many keys across multiple L0 files still compact correctly via streaming merge.
TEST(CompactionStreamTest, ManyKeysSurviveL0Compact) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 512;
    opts.level0_compaction_trigger = 2;
    opts.wal_sync = false;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());

    WriteOptions wo;
    wo.sync = false;
    const int N = 400;
    for (int i = 0; i < N; ++i) {
        ASSERT_TRUE(db->put(wo, "k" + std::to_string(i),
                            "v" + std::to_string(i) + "-xxxx").ok());
    }
    db->waitFlush();
    db->compact();
    db->waitCompaction();

    ReadOptions ro;
    for (int i = 0; i < N; i += 17) {
        std::string got;
        ASSERT_TRUE(db->get(ro, "k" + std::to_string(i), &got).ok()) << i;
        EXPECT_EQ(got, "v" + std::to_string(i) + "-xxxx");
    }
    rmTree(dir);
}
