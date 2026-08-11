// Module 11: Raft election + consistent hashing (C++ algorithm practice; real Module 11 often uses Go)
#include <cassert>
#include <cstdint>
#include <iostream>
#include <map>
#include <string>
#include <vector>

enum class RaftRole { Follower, Candidate, Leader };

struct RaftNode {
  int id = 0;
  RaftRole role = RaftRole::Follower;
  int current_term = 0;
  int voted_for = -1;
  int leader_id = -1;
};

// 确定性选举：候选人自增任期、自投一票，向其余节点拉票，达多数则成为 Leader。
bool TryElect(std::vector<RaftNode>& nodes, int candidate_id) {
  auto& c = nodes[candidate_id];
  c.role = RaftRole::Candidate;
  c.current_term += 1;
  c.voted_for = candidate_id;
  int term = c.current_term;
  int votes = 1;
  for (int i = 0; i < (int)nodes.size(); ++i) {
    if (i == candidate_id) continue;
    auto& n = nodes[i];
    if (n.current_term > term) continue;
    if (n.current_term < term) {
      n.current_term = term;
      n.voted_for = -1;
      n.role = RaftRole::Follower;
    }
    if (n.voted_for == -1 || n.voted_for == candidate_id) {
      n.voted_for = candidate_id;
      ++votes;
    }
  }
  int majority = (int)nodes.size() / 2 + 1;
  if (votes >= majority) {
    c.role = RaftRole::Leader;
    for (auto& n : nodes) {
      n.current_term = term;
      n.leader_id = candidate_id;
      if (n.id != candidate_id) n.role = RaftRole::Follower;
    }
    return true;
  }
  return false;
}

class ConsistentHash {
 public:
  ConsistentHash(std::vector<std::string> nodes, int vnodes) {
    for (const auto& node : nodes) {
      for (int i = 0; i < vnodes; ++i) {
        std::string vn = node + "#" + std::to_string(i);
        ring_[Hash(vn)] = node;
      }
    }
  }
  std::string Locate(const std::string& key) const {
    uint32_t h = Hash(key);
    auto it = ring_.lower_bound(h);
    if (it == ring_.end()) it = ring_.begin();
    return it->second;
  }
  static uint32_t Hash(const std::string& s) {
    uint32_t h = 2166136261u;
    for (unsigned char c : s) { h ^= c; h *= 16777619u; }
    return h;
  }
 private:
  std::map<uint32_t, std::string> ring_;
};

int main() {
  std::cout << "==== module11_raft_sharding ====\n";
  std::cout << "注：真实 Module 11 常用 Go；本文件为 C++ 算法练习。\n";
  std::vector<RaftNode> cluster(3); for (int i=0;i<3;++i) cluster[i].id=i;
  assert(TryElect(cluster, 1));
  assert(cluster[1].role == RaftRole::Leader);
  assert(cluster[0].role == RaftRole::Follower && cluster[2].role == RaftRole::Follower);
  assert(cluster[0].leader_id == 1);
  std::cout << "[OK] Raft election leader=1 term=" << cluster[1].current_term << "\n";

  ConsistentHash ch({"nodeA", "nodeB", "nodeC"}, 32);
  std::map<std::string, int> counts;
  for (int i = 0; i < 300; ++i) counts[ch.Locate("key-" + std::to_string(i))]++;
  assert(counts.size() == 3);
  for (auto& kv : counts) { assert(kv.second > 20); std::cout << "  " << kv.first << "=" << kv.second << "\n"; }
  assert(ch.Locate("user:42") == ch.Locate("user:42"));
  std::cout << "[OK] consistent hashing\nALL CHECKS PASSED\n";
  return 0;
}