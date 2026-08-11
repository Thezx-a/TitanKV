/*
 * ============================================================================
 * TitanKV 练习 - Module 02: C++ 核心基础（Slice / Status / Varint）
 * ============================================================================
 * 目标：
 *   1) 实现非拥有视图 Slice（类似 leveldb::Slice / string_view）
 *   2) 实现 Status（错误码 + 消息），贯穿 KV 引擎错误处理
 *   3) 实现 Varint64 编解码（空间紧凑的整数序列化）
 *
 * 构建：
 *   cmake -B build -S . && cmake --build build -j && ./build/module02_cpp_core
 * ============================================================================
 */

#include <cassert>
#include <cstdint>
#include <cstring>
#include <iostream>
#include <string>
#include <vector>

// ---------------------------------------------------------------------------
// Slice：非拥有的字节/字符串视图
// ---------------------------------------------------------------------------
// 设计要点：
// - 不负责内存分配与释放，只保存指针 + 长度
// - 生命周期必须短于底层缓冲区（或 string）
// - 比较、前缀判断是 KV / SST / WAL 中高频操作
class Slice {
 public:
  Slice() : data_(""), size_(0) {}
  Slice(const char* d, size_t n) : data_(d), size_(n) {}
  // 从 C 字符串构造：使用 strlen 得到长度（不含 '\0'）
  Slice(const char* s) : data_(s), size_(s ? std::strlen(s) : 0) {}
  Slice(const std::string& s) : data_(s.data()), size_(s.size()) {}

  const char* data() const { return data_; }
  size_t size() const { return size_; }
  bool empty() const { return size_ == 0; }

  // 按字典序比较：先比公共前缀，再比长度
  bool operator==(const Slice& other) const {
    return size_ == other.size_ &&
           (size_ == 0 || std::memcmp(data_, other.data_, size_) == 0);
  }
  bool operator!=(const Slice& other) const { return !(*this == other); }

  // 判断本 Slice 是否以 prefix 开头（常用于前缀扫描、布隆过滤键等）
  bool starts_with(const Slice& prefix) const {
    return size_ >= prefix.size_ &&
           (prefix.size_ == 0 ||
            std::memcmp(data_, prefix.data_, prefix.size_) == 0);
  }

  std::string ToString() const { return std::string(data_, size_); }

 private:
  const char* data_;
  size_t size_;
};

// ---------------------------------------------------------------------------
// Status：操作结果（成功或带错误码/消息）
// ---------------------------------------------------------------------------
// 设计要点：
// - ok() 表示成功；否则可通过 code()/message() 获取详情
// - 真实引擎里常用 status.ok() 短路返回，避免异常控制流
enum class StatusCode {
  kOk = 0,
  kNotFound,
  kCorruption,
  kInvalidArgument,
  kIOError,
};

class Status {
 public:
  Status() : code_(StatusCode::kOk), msg_() {}
  Status(StatusCode c, std::string msg) : code_(c), msg_(std::move(msg)) {}

  static Status OK() { return Status(); }
  static Status NotFound(const std::string& msg) {
    return Status(StatusCode::kNotFound, msg);
  }
  static Status Corruption(const std::string& msg) {
    return Status(StatusCode::kCorruption, msg);
  }
  static Status InvalidArgument(const std::string& msg) {
    return Status(StatusCode::kInvalidArgument, msg);
  }
  static Status IOError(const std::string& msg) {
    return Status(StatusCode::kIOError, msg);
  }

  bool ok() const { return code_ == StatusCode::kOk; }
  StatusCode code() const { return code_; }
  const std::string& message() const { return msg_; }

 private:
  StatusCode code_;
  std::string msg_;
};

// ---------------------------------------------------------------------------
// Varint64：变长整数编码（Protobuf / LevelDB 同款思路）
// ---------------------------------------------------------------------------
// 编码规则：
// - 每个字节的低 7 位存放数据，最高位(MSB)为 continuation bit
// - MSB=1 表示后面还有字节；MSB=0 表示本字节是最后一个
// - 小整数占用更少字节（例如 127 以下只需 1 字节）
//
// encodeVarint64：把 v 写入 dst，返回写入的字节数
// decodeVarint64：从 input 解码到 *value，返回消费的字节数；失败返回 0
size_t encodeVarint64(uint64_t v, char* dst) {
  unsigned char* ptr = reinterpret_cast<unsigned char*>(dst);
  size_t n = 0;
  while (v >= 0x80) {
    ptr[n++] = static_cast<unsigned char>(v) | 0x80;
    v >>= 7;
  }
  ptr[n++] = static_cast<unsigned char>(v);
  return n;
}

size_t decodeVarint64(const char* input, size_t len, uint64_t* value) {
  uint64_t result = 0;
  for (size_t i = 0; i < len && i < 10; ++i) {
    unsigned char byte = static_cast<unsigned char>(input[i]);
    result |= static_cast<uint64_t>(byte & 0x7f) << (7 * i);
    if ((byte & 0x80) == 0) {
      *value = result;
      return i + 1;
    }
  }
  return 0;  // 截断或非法
}

int main() {
  std::cout << "=== Module02: Slice / Status / Varint ===\n";

  // ---- Slice 演示 ----
  std::string backing = "hello-titankv";
  Slice s1(backing);
  Slice s2("hello");
  assert(!s1.empty());
  assert(s1.size() == backing.size());
  assert(s1.starts_with(s2));
  assert(s1.starts_with(Slice("hello-titankv")));
  assert(!(s2 == s1));
  Slice s3(backing.data(), 5);
  assert(s3 == s2);
  std::cout << "[Slice] starts_with / == 自检通过: " << s1.ToString() << "\n";

  // ---- Status 演示 ----
  Status ok = Status::OK();
  assert(ok.ok());
  Status nf = Status::NotFound("key missing");
  assert(!nf.ok());
  assert(nf.code() == StatusCode::kNotFound);
  assert(nf.message() == "key missing");
  std::cout << "[Status] OK 与 NotFound 自检通过\n";

  // ---- Varint 演示 ----
  const uint64_t samples[] = {0, 1, 127, 128, 300, 16384, UINT64_C(0xFFFFFFFFFFFFFFFF)};
  for (uint64_t v : samples) {
    char buf[16];
    size_t n = encodeVarint64(v, buf);
    assert(n >= 1 && n <= 10);
    uint64_t decoded = 0;
    size_t m = decodeVarint64(buf, n, &decoded);
    assert(m == n);
    assert(decoded == v);
  }
  // 截断输入应失败
  {
    char buf[16];
    size_t n = encodeVarint64(300, buf);
    assert(n > 1);
    uint64_t decoded = 0;
    assert(decodeVarint64(buf, 1, &decoded) == 0);
  }
  std::cout << "[Varint64] 编解码往返自检通过\n";

  std::cout << "module02_cpp_core SUCCESS\n";
  return 0;
}