/**
 * ============================================================================
 * Module 01 · 环境搭建与项目概览 —— 最小 KV 写路径调用链模拟
 * ============================================================================
 *
 * 背景：
 *   TitanKV / minikv 的一次 Put 大致会走：
 *     DB::Put → MemTable::Put →（阈值满）flushMemTable → SSTable
 *   本练习用极简类把这条调用链「画」出来，方便对照源码阅读。
 *
 * 编译运行：
 *   cmake -B build && cmake --build build -j && ./build/module01_overview
 */

#include <iostream>
#include <map>
#include <string>
#include <vector>

// ---------------------------------------------------------------------------
// MemTable：内存中的有序表（真实项目里是跳表）
// ---------------------------------------------------------------------------
class MemTable {
public:
    // 写入一条 KV；真实实现还会带 sequence number
    void Put(const std::string& key, const std::string& value) {
        std::cout << "  [MemTable::Put] key=\"" << key << "\" value=\"" << value << "\"\n";
        table_[key] = value;
    }

    // 点查：找不到返回空 optional 语义（这里用 bool）
    bool Get(const std::string& key, std::string* out) const {
        auto it = table_.find(key);
        if (it == table_.end()) return false;
        *out = it->second;
        return true;
    }

    std::size_t size() const { return table_.size(); }

    // 导出全部数据，供 flush 使用
    const std::map<std::string, std::string>& data() const { return table_; }

    void Clear() { table_.clear(); }

private:
    // 用 std::map 模拟「有序」特性；真实 MemTable = SkipList
    std::map<std::string, std::string> table_;
};

// ---------------------------------------------------------------------------
// SSTable：落盘后的不可变有序文件（这里只保存在内存 vector 里做演示）
// ---------------------------------------------------------------------------
class SSTable {
public:
    explicit SSTable(std::map<std::string, std::string> data)
        : data_(std::move(data)) {
        std::cout << "  [SSTable::Build] 写入 " << data_.size() << " 条记录到\"磁盘\"\n";
    }

    bool Get(const std::string& key, std::string* out) const {
        auto it = data_.find(key);
        if (it == data_.end()) return false;
        *out = it->second;
        return true;
    }

private:
    std::map<std::string, std::string> data_;
};

// ---------------------------------------------------------------------------
// MiniDB：串联 Put → MemTable → flush → SSTable 的调用链
// ---------------------------------------------------------------------------
class MiniDB {
public:
    // 写路径：先写 MemTable；超过阈值就 flush
    void Put(const std::string& key, const std::string& value) {
        std::cout << "[DB::Put] 开始\n";
        mem_.Put(key, value);

        // 真实引擎用字节数阈值；这里用条目数方便演示
        if (mem_.size() >= flush_threshold_) {
            flushMemTable();
        }
        std::cout << "[DB::Put] 结束\n";
    }

    // 读路径：先查 MemTable，再从新到旧查 SSTable
    bool Get(const std::string& key, std::string* out) const {
        std::cout << "[DB::Get] key=\"" << key << "\"\n";
        if (mem_.Get(key, out)) {
            std::cout << "  → 命中 MemTable\n";
            return true;
        }
        // 从最新的 SSTable 往回找（LSM 读路径常见顺序）
        for (auto it = sstables_.rbegin(); it != sstables_.rend(); ++it) {
            if (it->Get(key, out)) {
                std::cout << "  → 命中 SSTable\n";
                return true;
            }
        }
        std::cout << "  → 未找到\n";
        return false;
    }

private:
    // flushMemTable：把内存表变成不可变 SSTable
    // 对照 hellocpp：minikv 里同名函数会写 WAL 切分、建 SSTable、更新 Version
    void flushMemTable() {
        std::cout << "[DB::flushMemTable] MemTable 已满，开始刷盘\n";
        sstables_.emplace_back(mem_.data());
        mem_.Clear();
        std::cout << "[DB::flushMemTable] 完成，当前 SSTable 数=" << sstables_.size() << "\n";
    }

    MemTable mem_;
    std::vector<SSTable> sstables_;
    std::size_t flush_threshold_ = 3;  // 演示用：满 3 条就 flush
};

int main() {
    std::cout << "========================================\n";
    std::cout << " Module 01 · 最小 KV 调用链\n";
    std::cout << "========================================\n";

    MiniDB db;
    db.Put("apple", "红苹果");
    db.Put("banana", "香蕉");
    db.Put("cherry", "樱桃");  // 触发 flush
    db.Put("date", "枣");

    std::string value;
    if (db.Get("banana", &value)) {
        std::cout << "校验: banana => " << value << "\n";
    }
    if (db.Get("date", &value)) {
        std::cout << "校验: date => " << value << "\n";
    }
    if (!db.Get("missing", &value)) {
        std::cout << "校验: missing 不存在（符合预期）\n";
    }

    std::cout << "----------------------------------------\n";
    std::cout << "Module 01 通过 ✔\n";
    return 0;
}
