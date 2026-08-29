#include <gtest/gtest.h>

#include <atomic>
#include <cstdlib>
#include <string>
#include <unistd.h>

#include "core/compaction.h"
#include "minikv/db.h"
#include "minikv/options.h"
#include "utils/metrics.h"

using minikv::DB;
using minikv::Options;
using minikv::ReadOptions;
using minikv::WriteOptions;
using minikv::core::compactionRetryBackoffMs;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    return std::string(t) + "/titankv_cretry_" + std::to_string(::getpid()) +
           "_" + std::to_string(counter.fetch_add(1));
}

void rmTree(const std::string& root) {
    int rc = std::system(("rm -rf " + root).c_str());
    (void)rc;
}

}  // namespace

// E4: consecutive failure count → exponential backoff (capped).
TEST(CompactionRetryTest, BackoffGrowsThenCaps) {
    EXPECT_EQ(compactionRetryBackoffMs(0), 100);
    EXPECT_EQ(compactionRetryBackoffMs(1), 100);
    EXPECT_EQ(compactionRetryBackoffMs(2), 200);
    EXPECT_EQ(compactionRetryBackoffMs(3), 400);
    EXPECT_EQ(compactionRetryBackoffMs(6), 3200);
    EXPECT_EQ(compactionRetryBackoffMs(99), 3200);
}

// E4: injected merge failures bump Prometheus counter, then retry succeeds
// and keys remain readable (same path as real IOError during compact).
TEST(CompactionRetryTest, InjectedFailuresBumpMetricThenSucceed) {
    std::string dir = uniqueDir();
    Options opts;
    opts.db_path = dir;
    opts.memtable_size = 512;
    opts.level0_compaction_trigger = 2;
    opts.wal_sync = false;
    opts.compaction_fail_inject = 2;  // first 2 merges fail, then real work

    auto& m = minikv::utils::EngineMetrics::instance();
    const uint64_t fails0 = m.compaction_failures.load();

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());

    WriteOptions wo;
    wo.sync = false;
    for (int i = 0; i < 200; ++i) {
        ASSERT_TRUE(db->put(wo, "k" + std::to_string(i),
                            "v" + std::to_string(i) + "-pad").ok());
    }
    db->waitFlush();
    db->compact();
    db->waitCompaction();

    EXPECT_GE(m.compaction_failures.load(), fails0 + 2);

    std::string text = m.prometheusText();
    EXPECT_NE(text.find("titankv_engine_compaction_failures_total"), std::string::npos);

    ReadOptions ro;
    std::string got;
    ASSERT_TRUE(db->get(ro, "k0", &got).ok());
    EXPECT_EQ(got, "v0-pad");
    ASSERT_TRUE(db->get(ro, "k199", &got).ok());
    EXPECT_EQ(got, "v199-pad");

    rmTree(dir);
}
