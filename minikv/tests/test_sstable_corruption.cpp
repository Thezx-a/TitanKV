#include <gtest/gtest.h>

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cstdio>
#include <cstring>
#include <memory>
#include <string>
#include <vector>

#include "core/internal_key.h"
#include "core/sstable_builder.h"
#include "core/sstable_reader.h"
#include "minikv/db.h"
#include "minikv/options.h"
#include "minikv/slice.h"
#include "utils/coding.h"

using minikv::DB;
using minikv::Options;
using minikv::ReadOptions;
using minikv::Slice;
using minikv::Status;
using minikv::WriteOptions;
using minikv::core::CompressionType;
using minikv::core::InternalKeyEncode;
using minikv::core::PointLookup;
using minikv::core::SSTableBuilder;
using minikv::core::SSTableReader;
using minikv::core::ValueType;
using minikv::utils::encodeFixed32;

namespace {

std::string tmpPath(const char* tag) {
    const char* t = std::getenv("TMPDIR");
    if (!t || !*t) t = "/tmp";
    return std::string(t) + "/titankv_sst_corrupt_" + tag + "_" +
           std::to_string(::getpid());
}

void cleanup(const std::string& path) {
    ::unlink(path.c_str());
    ::unlink((path + ".bloom").c_str());
}

std::string buildOne(const std::string& path) {
    cleanup(path);
    SSTableBuilder b(path, /*block_size=*/4096, CompressionType::kNone);
    std::string uk = "alpha";
    std::string ik = InternalKeyEncode(uk, 1, ValueType::kValue);
    EXPECT_TRUE(b.add(Slice(ik), Slice(uk), Slice("bravo")).ok());
    EXPECT_TRUE(b.finish().ok());
    return path;
}

void flipPayloadByte(const std::string& path, size_t payload_off = 0) {
    int fd = ::open(path.c_str(), O_RDWR);
    ASSERT_GE(fd, 0);
    off_t at = static_cast<off_t>(13 + payload_off);
    ::lseek(fd, at, SEEK_SET);
    unsigned char c = 0;
    ASSERT_EQ(::read(fd, &c, 1), 1);
    c ^= 0xFF;
    ::lseek(fd, at, SEEK_SET);
    ASSERT_EQ(::write(fd, &c, 1), 1);
    ::close(fd);
}

std::vector<std::string> listSsts(const std::string& dir) {
    std::vector<std::string> out;
    for (int i = 1; i < 512; ++i) {
        for (const char* sub : {"level-0/", "level-1/", "level-2/", ""}) {
            std::string cand = dir + "/" + sub + std::to_string(i) + ".sst";
            if (::access(cand.c_str(), F_OK) == 0) out.push_back(cand);
        }
    }
    return out;
}

}  // namespace

TEST(SSTableCorruptionTest, FlippedBlockByteReturnsCorruption) {
    std::string path = tmpPath("flip");
    buildOne(path);
    flipPayloadByte(path);

    auto r = SSTableReader::open(path);
    ASSERT_NE(r, nullptr);

    std::string value;
    PointLookup pl = PointLookup::kMiss;
    Status s = r->lookup(Slice("alpha"), &value, &pl);
    EXPECT_TRUE(s.isCorruption()) << s.ToString();
    EXPECT_EQ(pl, PointLookup::kMiss);

    cleanup(path);
}

TEST(SSTableCorruptionTest, TruncatedFileDoesNotSilentMiss) {
    std::string path = tmpPath("trunc");
    buildOne(path);

    struct stat st;
    ASSERT_EQ(::stat(path.c_str(), &st), 0);
    ASSERT_GT(st.st_size, 20);
    ASSERT_EQ(::truncate(path.c_str(), st.st_size / 2), 0);

    auto r = SSTableReader::open(path);
    if (!r) {
        cleanup(path);
        return;
    }
    std::string value;
    PointLookup pl = PointLookup::kMiss;
    Status s = r->lookup(Slice("alpha"), &value, &pl);
    EXPECT_FALSE(s.ok()) << "expected IOError/Corruption, got " << s.ToString();
    EXPECT_TRUE(s.isCorruption() || s.isIOError()) << s.ToString();

    cleanup(path);
}

TEST(SSTableCorruptionTest, HugePayloadSizeRejected) {
    std::string path = tmpPath("huge");
    buildOne(path);

    int fd = ::open(path.c_str(), O_RDWR);
    ASSERT_GE(fd, 0);
    char forged[4];
    encodeFixed32(forged, 0x7FFFFFFFu);
    ::lseek(fd, 4, SEEK_SET);
    ASSERT_EQ(::write(fd, forged, 4), 4);
    ::lseek(fd, 8, SEEK_SET);
    ASSERT_EQ(::write(fd, forged, 4), 4);
    ::close(fd);

    auto r = SSTableReader::open(path);
    ASSERT_NE(r, nullptr);

    std::string value;
    PointLookup pl = PointLookup::kMiss;
    Status s = r->lookup(Slice("alpha"), &value, &pl);
    EXPECT_TRUE(s.isCorruption()) << s.ToString();

    cleanup(path);
}

// M5: DB::get must surface SST Corruption/IOError (not silent Ok/NotFound).
TEST(SSTableCorruptionTest, DbGetPropagatesSstCorruption) {
    std::string dir = tmpPath("db");
    {
        int rc = std::system(("rm -rf " + dir).c_str());
        (void)rc;
    }

    Options opts;
    opts.db_path = dir;
    opts.wal_sync = true;
    opts.max_level = 2;
    opts.memtable_size = 64;  // force flush generations
    opts.level0_compaction_trigger = 64;
    opts.compression = 0;
    opts.bloom_filter_enabled = false;

    {
        std::unique_ptr<DB> db;
        ASSERT_TRUE(DB::open(opts, &db).ok());
        WriteOptions wo;
        wo.sync = true;
        ASSERT_TRUE(db->put(wo, "aaa", "v1").ok());
        for (int i = 0; i < 40; ++i) {
            ASSERT_TRUE(db->put(wo, "zzz" + std::to_string(i),
                                std::string(32, 'x')).ok());
        }
        db->waitFlush();
    }

    auto ssts = listSsts(dir);
    ASSERT_FALSE(ssts.empty()) << "no SST after flush in " << dir;

    bool saw_direct_corruption = false;
    for (const auto& sst : ssts) {
        flipPayloadByte(sst);
        auto r = SSTableReader::open(sst);
        if (!r) continue;
        std::string vv;
        PointLookup pl = PointLookup::kMiss;
        Status ls = r->lookup(Slice("aaa"), &vv, &pl);
        if (ls.isCorruption() || ls.isIOError()) saw_direct_corruption = true;
    }
    ASSERT_TRUE(saw_direct_corruption)
        << "flip did not make any SST return Corruption on aaa";

    // Drop WALs so reopen cannot hide SST errors behind replay.
    {
        int rc = std::system(("rm -f " + dir + "/wal-*.log").c_str());
        (void)rc;
    }

    std::unique_ptr<DB> db;
    ASSERT_TRUE(DB::open(opts, &db).ok());

    std::string value;
    Status s = db->get(ReadOptions{}, "aaa", &value);
    EXPECT_FALSE(s.ok()) << "value=" << value << " status=" << s.ToString();
    EXPECT_TRUE(s.isCorruption() || s.isIOError()) << s.ToString();
    EXPECT_FALSE(s.isNotFound());

    db.reset();
    {
        int rc = std::system(("rm -rf " + dir).c_str());
        (void)rc;
    }
}
