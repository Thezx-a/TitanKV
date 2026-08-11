#include <gtest/gtest.h>

#include <atomic>
#include <cstdio>
#include <cstdlib>
#include <dirent.h>
#include <string>
#include <unistd.h>
#include <vector>
#include <algorithm>

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
    return std::string(t) + "/titankv_db_reopen_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

void rmTree(const std::string& root) {
    std::string cmd = "rm -rf " + root;
    int rc = std::system(cmd.c_str());
    (void)rc;
}

}  // namespace

// Regression: recover() used to append Manifest kReset without re-recording the
// restored SST list. First reopen still saw data via in-memory restore; second
// reopen replayed past kReset and lost flushed SST refs (WAL already truncated).
TEST(DBReopenTest, FlushedDataSurvivesDoubleReopen) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;  // force flush quickly
    opts.wal_sync = true;

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        WriteOptions wo;
        wo.sync = true;
        // Write enough to exceed memtable and flush (rotate to a new wal-<N>.log).
        for (int i = 0; i < 40; ++i) {
            std::string k = "k" + std::to_string(i);
            std::string v = "value-payload-" + std::to_string(i) + "-xxxxxxxx";
            ASSERT_TRUE(db->put(wo, k, v).ok()) << i;
        }
        std::string got;
        ASSERT_TRUE(db->get(ReadOptions{}, "k0", &got).ok());
        EXPECT_EQ(got, "value-payload-0-xxxxxxxx");
    }

    // First reopen (Manifest must still list SST).
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        Status s = db->get(ReadOptions{}, "k0", &got);
        ASSERT_TRUE(s.ok()) << s.message();
        EXPECT_EQ(got, "value-payload-0-xxxxxxxx");
        ASSERT_TRUE(db->get(ReadOptions{}, "k39", &got).ok());
        EXPECT_EQ(got, "value-payload-39-xxxxxxxx");
    }

    // Second reopen — this is where kReset used to wipe SST refs.
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        Status s = db->get(ReadOptions{}, "k0", &got);
        ASSERT_TRUE(s.ok()) << "second reopen lost flushed key: " << s.message();
        EXPECT_EQ(got, "value-payload-0-xxxxxxxx");
        ASSERT_TRUE(db->get(ReadOptions{}, "k20", &got).ok());
        EXPECT_EQ(got, "value-payload-20-xxxxxxxx");
    }

    rmTree(dir);
}

// After flush, the previous wal-<N>.log must be deleted and a newer wal file used.
TEST(DBReopenTest, FlushRotatesToNewWalFile) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;
    opts.wal_sync = true;

    auto listWals = [&]() {
        std::vector<std::string> names;
        DIR* d = ::opendir(dir.c_str());
        if (!d) return names;
        while (dirent* ent = ::readdir(d)) {
            std::string name = ent->d_name;
            if (name.rfind("wal-", 0) == 0 && name.size() > 4) names.push_back(name);
            if (name == "wal.log") names.push_back(name);
        }
        ::closedir(d);
        std::sort(names.begin(), names.end());
        return names;
    };

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        auto before = listWals();
        ASSERT_FALSE(before.empty());

        WriteOptions wo;
        wo.sync = true;
        for (int i = 0; i < 40; ++i) {
            ASSERT_TRUE(db->put(wo, "k" + std::to_string(i),
                                "value-payload-" + std::to_string(i) + "-xxxxxxxx").ok());
        }

        auto after = listWals();
        ASSERT_FALSE(after.empty());
        // Active WAL should be a numbered file, not legacy wal.log.
        EXPECT_EQ(after[0].rfind("wal-", 0), 0u);
        // Old generation must be gone after successful flush (exactly one live WAL).
        EXPECT_EQ(after.size(), 1u) << "expected single live WAL after flush";

        std::string got;
        ASSERT_TRUE(db->get(ReadOptions{}, "k0", &got).ok());
        EXPECT_EQ(got, "value-payload-0-xxxxxxxx");
    }

    // Unflushed tail in the live WAL must survive reopen.
    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        std::string got;
        ASSERT_TRUE(db->get(ReadOptions{}, "k0", &got).ok());
        EXPECT_EQ(got, "value-payload-0-xxxxxxxx");
    }

    rmTree(dir);
}
