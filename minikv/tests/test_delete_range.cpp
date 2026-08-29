#include <gtest/gtest.h>

#include <atomic>
#include <cstdio>
#include <cstdlib>
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
    uint64_t n = counter.fetch_add(1);
    return std::string(t) + "/titankv_delete_range_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

void rmTree(const std::string& root) {
    std::string cmd = "rm -rf " + root;
    int rc = std::system(cmd.c_str());
    (void)rc;
}

}  // namespace

// E5: DeleteRange removes all user keys in [start, end).
TEST(DeleteRangeTest, DeletesPrefixRange) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 1 << 20;
    opts.wal_sync = true;

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        WriteOptions wo;
        wo.sync = true;

        ASSERT_TRUE(db->put(wo, "wiki:page:demo:a", "A").ok());
        ASSERT_TRUE(db->put(wo, "wiki:page:demo:b", "B").ok());
        ASSERT_TRUE(db->put(wo, "wiki:edge:demo:a:b", "E").ok());
        ASSERT_TRUE(db->put(wo, "rag:chunk:demo:d:00000000", "C").ok());
        ASSERT_TRUE(db->put(wo, "keep:me", "alive").ok());

        // wipe wiki:page:demo: prefix → ["wiki:page:demo:", "wiki:page:demo:" + "\\xff")
        std::string start = "wiki:page:demo:";
        std::string end = start + "\xff";
        ASSERT_TRUE(db->deleteRange(wo, start, end).ok());

        std::string got;
        EXPECT_FALSE(db->get(ReadOptions{}, "wiki:page:demo:a", &got).ok());
        EXPECT_FALSE(db->get(ReadOptions{}, "wiki:page:demo:b", &got).ok());
        // edge prefix untouched by page delete
        ASSERT_TRUE(db->get(ReadOptions{}, "wiki:edge:demo:a:b", &got).ok());
        EXPECT_EQ(got, "E");
        ASSERT_TRUE(db->get(ReadOptions{}, "rag:chunk:demo:d:00000000", &got).ok());
        ASSERT_TRUE(db->get(ReadOptions{}, "keep:me", &got).ok());
        EXPECT_EQ(got, "alive");
    }

    rmTree(dir);
}

TEST(DeleteRangeTest, EmptyRangeIsOk) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());
    WriteOptions wo;
    Status s = db->deleteRange(wo, "zzz:", "zzz:\xff");
    EXPECT_TRUE(s.ok()) << s.message();

    rmTree(dir);
}

TEST(DeleteRangeTest, LargePrefixBatched) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 1 << 20;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());
    WriteOptions wo;

    const int N = 2500;
    for (int i = 0; i < N; ++i) {
        char buf[64];
        std::snprintf(buf, sizeof(buf), "wiki:raw:col:%05d", i);
        ASSERT_TRUE(db->put(wo, buf, "x").ok()) << i;
    }
    ASSERT_TRUE(db->put(wo, "other:key", "y").ok());

    std::string start = "wiki:raw:col:";
    std::string end = start + "\xff";
    ASSERT_TRUE(db->deleteRange(wo, start, end).ok());

    std::string got;
    for (int i = 0; i < N; i += 100) {
        char buf[64];
        std::snprintf(buf, sizeof(buf), "wiki:raw:col:%05d", i);
        EXPECT_FALSE(db->get(ReadOptions{}, buf, &got).ok()) << buf;
    }
    ASSERT_TRUE(db->get(ReadOptions{}, "other:key", &got).ok());
    EXPECT_EQ(got, "y");

    rmTree(dir);
}
