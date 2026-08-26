#include <gtest/gtest.h>

#include <string>
#include <vector>

#include "core/internal_key.h"
#include "core/memtable.h"
#include "core/memtable_iterator.h"
#include "core/skip_list.h"
#include "minikv/slice.h"

using minikv::Slice;
using namespace minikv::core;

namespace {

std::string ik(const std::string& user, uint64_t seq,
               ValueType type = ValueType::kValue) {
    return InternalKeyEncode(user, seq, type);
}

}  // namespace

// M9: SkipList must expose O(log n) lowerBound by internal key.
TEST(SkipListSeekTest, FindGreaterOrEqualHitsFirstMatchingUserKey) {
    SkipList sl;
    sl.put(ik("a", 1), "va1");
    sl.put(ik("b", 5), "vb5");
    sl.put(ik("b", 9), "vb9");  // newer sorts before b@5
    sl.put(ik("c", 1), "vc1");

    // Seek to max-seq form of "b" → land on newest b (seq 9).
    std::string seek = InternalKeyEncode("b", kMaxSequenceNumber, ValueType::kValue);
    auto* node = sl.findGreaterOrEqual(Slice(seek));
    ASSERT_NE(node, nullptr);
    EXPECT_EQ(InternalKeyUserKey(Slice(node->key)).toString(), "b");
    EXPECT_EQ(InternalKeySequence(Slice(node->key)), 9u);
    EXPECT_EQ(node->value, "vb9");
}

TEST(SkipListSeekTest, FindGreaterOrEqualPastEndIsNull) {
    SkipList sl;
    sl.put(ik("a", 1), "va");
    std::string seek = InternalKeyEncode("zzz", kMaxSequenceNumber, ValueType::kValue);
    EXPECT_EQ(sl.findGreaterOrEqual(Slice(seek)), nullptr);
}

TEST(SkipListSeekTest, IteratorSeekAndNext) {
    SkipList sl;
    sl.put(ik("a", 1), "va");
    sl.put(ik("b", 1), "vb");
    sl.put(ik("c", 1), "vc");

    SkipList::Iterator it(&sl);
    it.seek(Slice(InternalKeyEncode("b", kMaxSequenceNumber, ValueType::kValue)));
    ASSERT_TRUE(it.valid());
    EXPECT_EQ(InternalKeyUserKey(it.key()).toString(), "b");
    it.next();
    ASSERT_TRUE(it.valid());
    EXPECT_EQ(InternalKeyUserKey(it.key()).toString(), "c");
    it.next();
    EXPECT_FALSE(it.valid());
}

// M9: MemTable::lookup must not full-scan entries(); newest / tombstone correct.
TEST(MemTableSeekTest, NewestVersionWins) {
    MemTable mt(4 * 1024 * 1024);
    mt.put(Slice("k"), Slice("v1"), 1, false);
    mt.put(Slice("k"), Slice("v2"), 2, false);
    mt.put(Slice("k"), Slice("v3"), 3, false);
    std::string out;
    EXPECT_EQ(mt.lookup(Slice("k"), 0, &out), PointLookup::kValue);
    EXPECT_EQ(out, "v3");
}

TEST(MemTableSeekTest, TombstoneAfterPuts) {
    MemTable mt(4 * 1024 * 1024);
    mt.put(Slice("k"), Slice("v1"), 1, false);
    mt.put(Slice("k"), Slice(""), 2, true);
    std::string out;
    EXPECT_EQ(mt.lookup(Slice("k"), 0, &out), PointLookup::kTombstone);
    EXPECT_FALSE(mt.get(Slice("k"), 0).has_value());
}

TEST(MemTableSeekTest, MissAndNeighborKeys) {
    MemTable mt(4 * 1024 * 1024);
    mt.put(Slice("a"), Slice("va"), 1, false);
    mt.put(Slice("c"), Slice("vc"), 1, false);
    EXPECT_EQ(mt.lookup(Slice("b"), 0, nullptr), PointLookup::kMiss);
    std::string out;
    EXPECT_EQ(mt.lookup(Slice("a"), 0, &out), PointLookup::kValue);
    EXPECT_EQ(out, "va");
}

TEST(MemTableSeekTest, LargeTablePointLookup) {
    MemTable mt(64 * 1024 * 1024);
    const int N = 20000;
    for (int i = 0; i < N; ++i) {
        std::string k = "key_" + std::to_string(i);
        mt.put(Slice(k), Slice("val_" + std::to_string(i)), static_cast<uint64_t>(i + 1),
               false);
    }
    // Overwrite a middle key with a newer seq.
    mt.put(Slice("key_100"), Slice("updated"), static_cast<uint64_t>(N + 10), false);
    auto v = mt.get(Slice("key_100"), 0);
    ASSERT_TRUE(v.has_value());
    EXPECT_EQ(*v, "updated");
    EXPECT_FALSE(mt.get(Slice("missing"), 0).has_value());
}

TEST(MemTableSeekTest, LazyIteratorSeek) {
    auto mt = std::make_shared<MemTable>(4 * 1024 * 1024);
    mt->put(Slice("a"), Slice("va"), 1, false);
    mt->put(Slice("b"), Slice("vb"), 1, false);
    mt->put(Slice("c"), Slice("vc"), 1, false);

    MemTableIterator it(mt);
    it.seek(Slice(InternalKeyEncode("b", kMaxSequenceNumber, ValueType::kValue)));
    ASSERT_TRUE(it.valid());
    EXPECT_EQ(InternalKeyUserKey(it.key()).toString(), "b");
    EXPECT_EQ(it.value().toString(), "vb");
    it.next();
    ASSERT_TRUE(it.valid());
    EXPECT_EQ(InternalKeyUserKey(it.key()).toString(), "c");
}
