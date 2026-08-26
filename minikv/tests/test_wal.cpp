#include <gtest/gtest.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/stat.h>
#include "core/wal.h"
using namespace minikv::core;

TEST(WALTest, AppendAndReplay) {
    std::string path = "/tmp/minikv_wal_test.log";
    ::unlink(path.c_str());
    { WAL wal(path); wal.append(minikv::Slice("r1")); wal.append(minikv::Slice("r2")); wal.sync(); }
    { WAL wal(path); auto r = wal.replay(); ASSERT_EQ(r.size(), 2u); EXPECT_EQ(r[0], "r1"); }
    ::unlink(path.c_str());
}

TEST(WALTest, Truncate) {
    std::string path = "/tmp/minikv_wal_trunc.log";
    ::unlink(path.c_str());
    {
        WAL wal(path);
        ASSERT_TRUE(wal.append(minikv::Slice("data")).ok());
        ASSERT_TRUE(wal.truncate().ok());
        ASSERT_TRUE(wal.append(minikv::Slice("after")).ok());
        ASSERT_TRUE(wal.sync().ok());
    }
    {
        WAL wal(path);
        auto r = wal.replay();
        ASSERT_EQ(r.size(), 1u);
        EXPECT_EQ(r[0], "after");
    }
    ::unlink(path.c_str());
}

// [P0-2] Torn tail must be ftruncated so later appends remain reachable on reopen.
TEST(WALTest, ReplayTruncatesTornTailAndAllowsAppend) {
    std::string path = "/tmp/minikv_wal_torn.log";
    ::unlink(path.c_str());

    {
        WAL wal(path);
        ASSERT_TRUE(wal.append(minikv::Slice("good-1")).ok());
        ASSERT_TRUE(wal.append(minikv::Slice("good-2")).ok());
        ASSERT_TRUE(wal.sync().ok());
    }

    struct stat st_before;
    ASSERT_EQ(::stat(path.c_str(), &st_before), 0);
    {
        int fd = ::open(path.c_str(), O_RDWR);
        ASSERT_GE(fd, 0);
        ASSERT_EQ(::ftruncate(fd, st_before.st_size - 5), 0);
        ::close(fd);
    }

    {
        WAL wal(path);
        auto r = wal.replay();
        ASSERT_EQ(r.size(), 1u) << "only the first complete record should survive";
        EXPECT_EQ(r[0], "good-1");

        struct stat st;
        ASSERT_EQ(::stat(path.c_str(), &st), 0);
        EXPECT_LT(st.st_size, st_before.st_size);
        EXPECT_GT(st.st_size, 0);

        ASSERT_TRUE(wal.append(minikv::Slice("good-3")).ok());
        ASSERT_TRUE(wal.sync().ok());
    }

    {
        WAL wal(path);
        auto r = wal.replay();
        ASSERT_EQ(r.size(), 2u);
        EXPECT_EQ(r[0], "good-1");
        EXPECT_EQ(r[1], "good-3");
    }
    ::unlink(path.c_str());
}
