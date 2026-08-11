/*
 * ============================================================================
 * TitanKV 练习 - Module 07: 迷你 LSM 引擎
 * ============================================================================
 * 目标：
 *   1) WAL：追加写入文件，崩溃后从 WAL 恢复
 *   2) MemTable：内存 map
 *   3) Flush：MemTable 刷成简易 SSTable（每行 key\tvalue）
 *   4) DB Put/Get：先查 MemTable，再查 SST；启动时 recover
 *
 * 数据目录：./lsm_demo_data（可清理）
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module07_lsm_engine
 * ============================================================================
 */

#include <cassert>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <map>
#include <sstream>
#include <string>
#include <utility>
#include <vector>

namespace fs = std::filesystem;

// ---------------------------------------------------------------------------
// WAL：Write-Ahead Log，先记日志再改内存，保证可恢复
// ---------------------------------------------------------------------------
class WAL {
 public:
  explicit WAL(fs::path path) : path_(std::move(path)) {
    out_.open(path_, std::ios::app | std::ios::binary);
    assert(out_.is_open());
  }

  // 记录一行：PUT\tkey\tvalue\n
  void AppendPut(const std::string& key, const std::string& value) {
    out_ << "PUT\t" << key << "\t" << value << "\n";
    out_.flush();
  }

  void Close() {
    if (out_.is_open()) out_.close();
  }

  // 从文件重放所有 PUT 到 memtable
  static void Recover(const fs::path& path, std::map<std::string, std::string>* mem) {
    if (!fs::exists(path)) return;
    std::ifstream in(path);
    std::string line;
    while (std::getline(in, line)) {
      if (line.empty()) continue;
      std::istringstream iss(line);
      std::string op, key, value;
      if (!std::getline(iss, op, '\t')) continue;
      if (!std::getline(iss, key, '\t')) continue;
      if (!std::getline(iss, value)) continue;
      if (op == "PUT") {
        (*mem)[key] = value;
      }
    }
  }

 private:
  fs::path path_;
  std::ofstream out_;
};

// ---------------------------------------------------------------------------
// SSTable：极简落盘格式，按 key 排序后一行一个键值
// ---------------------------------------------------------------------------
class SSTable {
 public:
  static void Write(const fs::path& path,
                    const std::map<std::string, std::string>& data) {
    std::ofstream out(path);
    assert(out.is_open());
    for (const auto& kv : data) {
      out << kv.first << "\t" << kv.second << "\n";
    }
  }

  static bool Get(const fs::path& path, const std::string& key, std::string* value) {
    if (!fs::exists(path)) return false;
    std::ifstream in(path);
    std::string line;
    while (std::getline(in, line)) {
      auto tab = line.find('\t');
      if (tab == std::string::npos) continue;
      std::string k = line.substr(0, tab);
      if (k == key) {
        *value = line.substr(tab + 1);
        return true;
      }
    }
    return false;
  }
};

// ---------------------------------------------------------------------------
// MiniDB：MemTable + WAL + 可选 flush 到 SST
// ---------------------------------------------------------------------------
class MiniDB {
 public:
  explicit MiniDB(fs::path dir) : dir_(std::move(dir)), wal_path_(dir_ / "wal.log"),
                                  sst_path_(dir_ / "level0.sst"), wal_(nullptr) {
    fs::create_directories(dir_);
    // 恢复顺序：先 SST，再 WAL（WAL 更新）
    LoadSSTIntoMem();
    WAL::Recover(wal_path_, &mem_);
    wal_ = new WAL(wal_path_);
  }

  ~MiniDB() {
    if (wal_) {
      wal_->Close();
      delete wal_;
    }
  }

  MiniDB(const MiniDB&) = delete;
  MiniDB& operator=(const MiniDB&) = delete;

  void Put(const std::string& key, const std::string& value) {
    wal_->AppendPut(key, value);
    mem_[key] = value;
  }

  bool Get(const std::string& key, std::string* value) const {
    auto it = mem_.find(key);
    if (it != mem_.end()) {
      *value = it->second;
      return true;
    }
    // Mem 未命中时再查 SST（本教学版恢复时已并入 mem，这里作兜底演示）
    return SSTable::Get(sst_path_, key, value);
  }

  // 将 MemTable 刷盘为 SST，并截断 WAL
  void Flush() {
    SSTable::Write(sst_path_, mem_);
    wal_->Close();
    delete wal_;
    // 截断 WAL：重新创建空文件
    {
      std::ofstream trunc(wal_path_, std::ios::trunc);
    }
    wal_ = new WAL(wal_path_);
  }

  size_t mem_size() const { return mem_.size(); }

 private:
  void LoadSSTIntoMem() {
    if (!fs::exists(sst_path_)) return;
    std::ifstream in(sst_path_);
    std::string line;
    while (std::getline(in, line)) {
      auto tab = line.find('\t');
      if (tab == std::string::npos) continue;
      mem_[line.substr(0, tab)] = line.substr(tab + 1);
    }
  }

  fs::path dir_;
  fs::path wal_path_;
  fs::path sst_path_;
  std::map<std::string, std::string> mem_;
  WAL* wal_;
};

int main() {
  std::cout << "=== Module07: Mini LSM (WAL/MemTable/SST) ===\n";

  const fs::path data_dir = fs::path("./lsm_demo_data");
  // 清理旧数据，保证演示可重复
  std::error_code ec;
  fs::remove_all(data_dir, ec);
  fs::create_directories(data_dir);

  {
    MiniDB db(data_dir);
    db.Put("a", "1");
    db.Put("b", "2");
    db.Put("c", "3");
    std::string v;
    assert(db.Get("a", &v) && v == "1");
    assert(db.Get("b", &v) && v == "2");
    db.Flush();
    assert(db.mem_size() == 3);
    std::cout << "[DB] Put/Get/Flush 自检通过\n";
  }

  // 模拟重启：仅有 SST +（空）WAL，应仍能读到数据
  {
    MiniDB db2(data_dir);
    std::string v;
    assert(db2.Get("a", &v) && v == "1");
    assert(db2.Get("c", &v) && v == "3");
    // 再写一条只进 WAL
    db2.Put("d", "4");
    std::cout << "[DB] 重启后从 SST 恢复自检通过\n";
  }

  // 再次重启：应通过 WAL 恢复到 d
  {
    MiniDB db3(data_dir);
    std::string v;
    assert(db3.Get("d", &v) && v == "4");
    assert(db3.Get("a", &v) && v == "1");
    std::cout << "[DB] WAL 恢复自检通过\n";
  }

  fs::remove_all(data_dir, ec);
  std::cout << "module07_lsm_engine SUCCESS\n";
  return 0;
}