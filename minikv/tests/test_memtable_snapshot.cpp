#include <gtest/gtest.h>

#include <atomic>
#include <cstdlib>
#include <string>
#include <thread>
#include <unistd.h>
#include <vector>

#include "minikv/db.h"
#include "minikv/iterator.h"
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
    return std::string(t) + "/titankv_memtable_snap_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

void rmTree(const std::string& root) {
    std::string cmd = "rm -rf " + root;
    int rc = std::system(cmd.c_str());
    (void)rc;
}

}  // namespace

// Get/Iterator copy shared_ptr then drop write_mutex_ before lookup, so a
// flush that moves/resets the DB members must not UAF the old MemTable.
TEST(MemTableSnapshotTest, ConcurrentGetAndIteratorDuringFlush) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 256;  // force frequent flush
    opts.wal_sync = false;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());

    WriteOptions wo;
    wo.sync = false;
    const int kSeed = 32;
    for (int i = 0; i < kSeed; ++i) {
        ASSERT_TRUE(db->put(wo, "seed" + std::to_string(i),
                            "seed-val-" + std::to_string(i) + "-xxxxxxxx").ok());
    }

    std::atomic<bool> stop{false};
    std::atomic<int> get_ok{0};
    std::atomic<int> get_fail{0};
    std::atomic<int> iter_scans{0};

    auto reader = [&]() {
        ReadOptions ro;
        while (!stop.load(std::memory_order_relaxed)) {
            for (int i = 0; i < kSeed; ++i) {
                std::string got;
                Status s = db->get(ro, "seed" + std::to_string(i), &got);
                if (s.ok() && got == "seed-val-" + std::to_string(i) + "-xxxxxxxx") {
                    get_ok.fetch_add(1, std::memory_order_relaxed);
                } else {
                    get_fail.fetch_add(1, std::memory_order_relaxed);
                }
            }
            auto it = db->newIterator(ro);
            int n = 0;
            for (it->seekToFirst(); it->valid(); it->next()) {
                ++n;
            }
            if (n > 0) iter_scans.fetch_add(1, std::memory_order_relaxed);
        }
    };

    std::thread r1(reader);
    std::thread r2(reader);

    for (int i = 0; i < 80; ++i) {
        ASSERT_TRUE(db->put(wo, "hot" + std::to_string(i),
                            "hot-val-" + std::to_string(i) + "-xxxxxxxx").ok());
    }

    stop.store(true, std::memory_order_relaxed);
    r1.join();
    r2.join();

    EXPECT_EQ(get_fail.load(), 0);
    EXPECT_GT(get_ok.load(), 0);
    EXPECT_GT(iter_scans.load(), 0);

    std::string got;
    ASSERT_TRUE(db->get(ReadOptions{}, "seed0", &got).ok());
    EXPECT_EQ(got, "seed-val-0-xxxxxxxx");
    ASSERT_TRUE(db->get(ReadOptions{}, "hot79", &got).ok());
    EXPECT_EQ(got, "hot-val-79-xxxxxxxx");

    rmTree(dir);
}

// Tombstone still returns NotFound (MemTable::get yields nullopt, Get continues).
TEST(MemTableSnapshotTest, DeleteTombstoneIsNotFound) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 4 * 1024 * 1024;

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());
    WriteOptions wo;
    ASSERT_TRUE(db->put(wo, "k", "v").ok());
    ASSERT_TRUE(db->del(wo, "k").ok());
    std::string got;
    Status s = db->get(ReadOptions{}, "k", &got);
    EXPECT_TRUE(s.isNotFound()) << s.message();

    rmTree(dir);
}
