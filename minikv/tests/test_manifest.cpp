#include <gtest/gtest.h>

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <atomic>
#include <cstdio>
#include <string>

#include "core/manifest.h"

using minikv::core::Manifest;
using minikv::Status;

namespace {

std::string uniqueDir() {
    const char* t = std::getenv("TMPDIR");
    if (!t || *t == '\0') t = "/tmp";
    static std::atomic<uint64_t> counter{0};
    uint64_t n = counter.fetch_add(1);
    return std::string(t) + "/titankv_manifest_test_" +
           std::to_string(::getpid()) + "_" + std::to_string(n);
}

void cleanup(const std::string& d) {
    if (d.empty()) return;
    std::string manifest = d + "/MANIFEST";
    ::unlink(manifest.c_str());
    ::rmdir(d.c_str());
}

}  // namespace

TEST(ManifestTest, FreshOpenIsEmpty) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir);
        Status s = m.open();
        ASSERT_TRUE(s.ok()) << s.message();
        EXPECT_TRUE(m.levels().empty() || m.totalFiles() == 0);
    }
    cleanup(dir);
}

TEST(ManifestTest, RoundTrip) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/100.sst", 100).ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/101.sst", 101).ok());
        ASSERT_TRUE(m.recordAddFile(1, "/data/200.sst", 200).ok());
        m.sync();
        EXPECT_EQ(m.levels().at(0).size(), 2u);
        EXPECT_EQ(m.levels().at(1).size(), 1u);
    }
    // Reopen and replay.
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        EXPECT_EQ(m.levels().size(), 8u);  // default 7 levels + 1 spare
        ASSERT_EQ(m.levels().at(0).size(), 2u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/100.sst");
        EXPECT_EQ(m.levels().at(0)[0].file_number, 100u);
        EXPECT_EQ(m.levels().at(0)[1].path, "/data/101.sst");
        EXPECT_EQ(m.levels().at(1).size(), 1u);
        EXPECT_EQ(m.levels().at(1)[0].path, "/data/200.sst");
    }
    cleanup(dir);
}

TEST(ManifestTest, RemovePersists) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/a.sst", 1).ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/b.sst", 2).ok());
        ASSERT_TRUE(m.recordRemoveFile(0, "/data/a.sst", 1).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/b.sst");
        EXPECT_EQ(m.levels().at(0)[0].file_number, 2u);
    }
    cleanup(dir);
}

TEST(ManifestTest, ResetClearsState) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/x.sst", 5).ok());
        ASSERT_TRUE(m.recordReset().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/y.sst", 7).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/y.sst");
    }
    cleanup(dir);
}

TEST(ManifestTest, TruncatedTailRecordIsIgnored) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    std::string manifest = dir + "/MANIFEST";
    off_t size_before = 0;
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/good.sst", 1).ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/part.sst", 2).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    // Truncate the MANIFEST file to simulate a torn write at the end.
    struct stat st;
    ::stat(manifest.c_str(), &st);
    size_before = st.st_size;
    int fd = ::open(manifest.c_str(), O_WRONLY);
    ::ftruncate(fd, st.st_size - 3);  // bite off 3 bytes of the last record
    ::close(fd);

    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        // Only the first record should be visible.
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/good.sst");

        // [P0-3] File must be truncated to last good offset.
        struct stat st2;
        ASSERT_EQ(::stat(manifest.c_str(), &st2), 0);
        EXPECT_LT(st2.st_size, size_before);

        // Append after self-heal must survive reopen.
        ASSERT_TRUE(m.recordAddFile(0, "/data/new.sst", 3).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_EQ(m.levels().at(0).size(), 2u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/good.sst");
        EXPECT_EQ(m.levels().at(0)[1].path, "/data/new.sst");
    }
    cleanup(dir);
}

// rewriteSnapshot archives the old log and writes a compact kAdd-only MANIFEST.
TEST(ManifestTest, RewriteSnapshotCompactsAndKeepsLiveFiles) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/a.sst", 1).ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/b.sst", 2).ok());
        ASSERT_TRUE(m.recordRemoveFile(0, "/data/a.sst", 1).ok());
        ASSERT_TRUE(m.recordAddFile(1, "/data/c.sst", 3).ok());
        ASSERT_TRUE(m.sync().ok());
        ASSERT_TRUE(m.rewriteSnapshot().ok());
        // Live set: b @ L0, c @ L1
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/b.sst");
        ASSERT_EQ(m.levels().at(1).size(), 1u);
        EXPECT_EQ(m.levels().at(1)[0].path, "/data/c.sst");
    }
    // Archived log should exist; active MANIFEST replays to the same live set.
    {
        struct stat st;
        EXPECT_EQ(::stat((dir + "/MANIFEST.bak").c_str(), &st), 0);
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        EXPECT_EQ(m.levels().at(0)[0].path, "/data/b.sst");
        ASSERT_EQ(m.levels().at(1).size(), 1u);
        EXPECT_EQ(m.levels().at(1)[0].path, "/data/c.sst");
        // Further edits append to the new file.
        ASSERT_TRUE(m.recordAddFile(0, "/data/d.sst", 4).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    {
        Manifest m(dir);
        ASSERT_TRUE(m.open().ok());
        ASSERT_EQ(m.levels().at(0).size(), 2u);
        EXPECT_EQ(m.levels().at(0)[1].path, "/data/d.sst");
    }
    // cleanup also removes bak if present
    ::unlink((dir + "/MANIFEST.bak").c_str());
    cleanup(dir);
}


// [P0-3] Opening a DB whose MANIFEST spans more levels than config must not
// crash or drop files — keep on-disk span and warn.
TEST(ManifestTest, OpenWithSmallerConfiguredLevelsKeepsData) {
    std::string dir = uniqueDir();
    ::mkdir(dir.c_str(), 0755);
    {
        Manifest m(dir, /*level_count=*/8);
        ASSERT_TRUE(m.open().ok());
        ASSERT_TRUE(m.recordAddFile(0, "/data/a.sst", 1).ok());
        ASSERT_TRUE(m.recordAddFile(5, "/data/deep.sst", 2).ok());
        ASSERT_TRUE(m.sync().ok());
    }
    {
        Manifest m(dir, /*level_count=*/3);  // smaller than on-disk span
        ASSERT_TRUE(m.open().ok());
        ASSERT_GE(m.levels().size(), 6u);
        ASSERT_EQ(m.levels().at(0).size(), 1u);
        ASSERT_EQ(m.levels().at(5).size(), 1u);
        EXPECT_EQ(m.levels().at(5)[0].path, "/data/deep.sst");
    }
    cleanup(dir);
}
