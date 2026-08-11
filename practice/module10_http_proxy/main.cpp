// =============================================================================
// Module 10: HTTP 代理相关练习
// 1) HTTP/1.1 请求解析状态机（支持分块 feed）
// 2) Nginx Smooth Weighted Round-Robin
// 3) 简易连接池 acquire/release
// =============================================================================
#include <cassert>
#include <iostream>
#include <optional>
#include <string>
#include <unordered_map>
#include <vector>

// --------------------- HTTP/1.1 请求解析状态机 ---------------------
enum class ParseState {
  kRequestLine,
  kHeaders,
  kBody,
  kDone,
  kError
};

struct HttpRequest {
  std::string method;
  std::string path;
  std::string version;
  std::unordered_map<std::string, std::string> headers;
  std::string body;
};

class HttpRequestParser {
 public:
  // 喂入任意长度的不完整数据；返回 true 表示已解析完整个请求
  bool feed(const char* data, size_t len) {
    buf_.append(data, len);
    while (state_ != ParseState::kDone && state_ != ParseState::kError) {
      if (state_ == ParseState::kRequestLine) {
        auto pos = buf_.find("\r\n");
        if (pos == std::string::npos) return false;
        std::string line = buf_.substr(0, pos);
        buf_.erase(0, pos + 2);
        // METHOD SP PATH SP VERSION
        auto s1 = line.find(' ');
        auto s2 = line.rfind(' ');
        if (s1 == std::string::npos || s2 == s1) {
          state_ = ParseState::kError;
          return false;
        }
        req_.method = line.substr(0, s1);
        req_.path = line.substr(s1 + 1, s2 - s1 - 1);
        req_.version = line.substr(s2 + 1);
        state_ = ParseState::kHeaders;
      } else if (state_ == ParseState::kHeaders) {
        auto pos = buf_.find("\r\n");
        if (pos == std::string::npos) return false;
        if (pos == 0) {
          buf_.erase(0, 2);
          // Content-Length?
          auto it = req_.headers.find("Content-Length");
          if (it != req_.headers.end()) {
            body_remain_ = static_cast<size_t>(std::stoul(it->second));
            state_ = body_remain_ > 0 ? ParseState::kBody : ParseState::kDone;
          } else {
            state_ = ParseState::kDone;
          }
          continue;
        }
        std::string line = buf_.substr(0, pos);
        buf_.erase(0, pos + 2);
        auto colon = line.find(':');
        if (colon == std::string::npos) {
          state_ = ParseState::kError;
          return false;
        }
        std::string key = line.substr(0, colon);
        std::string val = line.substr(colon + 1);
        // trim 前导空格
        size_t i = 0;
        while (i < val.size() && val[i] == ' ') ++i;
        val = val.substr(i);
        req_.headers[key] = val;
      } else if (state_ == ParseState::kBody) {
        if (buf_.size() < body_remain_) return false;
        req_.body = buf_.substr(0, body_remain_);
        buf_.erase(0, body_remain_);
        body_remain_ = 0;
        state_ = ParseState::kDone;
      }
    }
    return state_ == ParseState::kDone;
  }

  bool ok() const { return state_ == ParseState::kDone; }
  const HttpRequest& request() const { return req_; }

 private:
  ParseState state_ = ParseState::kRequestLine;
  HttpRequest req_;
  std::string buf_;
  size_t body_remain_ = 0;
};

// --------------------- Smooth Weighted Round-Robin (Nginx) ---------------------
// current_weight += effective_weight; 选最大; current_weight -= total
struct Upstream {
  std::string name;
  int weight;
  int current_weight = 0;
  int effective_weight;
  Upstream(std::string n, int w) : name(std::move(n)), weight(w), effective_weight(w) {}
};

class SmoothWRR {
 public:
  explicit SmoothWRR(std::vector<Upstream> ups) : ups_(std::move(ups)) {
    for (auto& u : ups_) total_ += u.effective_weight;
  }

  std::string next() {
    assert(!ups_.empty());
    Upstream* best = nullptr;
    for (auto& u : ups_) {
      u.current_weight += u.effective_weight;
      if (!best || u.current_weight > best->current_weight) best = &u;
    }
    assert(best);
    best->current_weight -= total_;
    return best->name;
  }

 private:
  std::vector<Upstream> ups_;
  int total_ = 0;
};

// --------------------- 连接池草图 ---------------------
class ConnPool {
 public:
  explicit ConnPool(size_t max_size) : max_(max_size) {}

  // 获取一个连接 id；池空且未满则新建；已满返回 nullopt
  std::optional<int> acquire() {
    if (!idle_.empty()) {
      int id = idle_.back();
      idle_.pop_back();
      ++in_use_;
      return id;
    }
    if (created_ >= max_) return std::nullopt;
    int id = static_cast<int>(++created_);
    ++in_use_;
    return id;
  }

  void release(int id) {
    assert(in_use_ > 0);
    --in_use_;
    idle_.push_back(id);
  }

  size_t in_use() const { return in_use_; }
  size_t idle() const { return idle_.size(); }
  size_t created() const { return created_; }

 private:
  size_t max_;
  size_t created_ = 0;
  size_t in_use_ = 0;
  std::vector<int> idle_;
};

int main() {
  std::cout << "==== module10_http_proxy ====\n";

  // 分块喂入不完整请求
  HttpRequestParser p;
  std::string chunk1 = "POST /api HTTP/1.1\r\nHost: ex";
  std::string chunk2 = "ample.com\r\nContent-Length: 5\r\n\r\nHel";
  std::string chunk3 = "lo";
  assert(!p.feed(chunk1.data(), chunk1.size()));
  assert(!p.feed(chunk2.data(), chunk2.size()));
  assert(p.feed(chunk3.data(), chunk3.size()));
  assert(p.ok());
  assert(p.request().method == "POST");
  assert(p.request().path == "/api");
  assert(p.request().headers.at("Host") == "example.com");
  assert(p.request().body == "Hello");
  std::cout << "[OK] HTTP parser incremental feed\n";

  // SWRR: a:5, b:1, c:1 → 7 次选取序列
  SmoothWRR rr({{"a", 5}, {"b", 1}, {"c", 1}});
  std::string seq;
  for (int i = 0; i < 7; ++i) {
    if (i) seq += ",";
    seq += rr.next();
  }
  // Nginx 经典序列：a,a,b,a,c,a,a
  assert(seq == "a,a,b,a,c,a,a");
  std::cout << "[OK] SWRR sequence: " << seq << "\n";

  ConnPool pool(2);
  auto c1 = pool.acquire();
  auto c2 = pool.acquire();
  auto c3 = pool.acquire();
  assert(c1 && c2 && !c3);
  pool.release(*c1);
  assert(pool.idle() == 1);
  auto c4 = pool.acquire();
  assert(c4 && *c4 == *c1);  // 复用
  std::cout << "[OK] connection pool acquire/release\n";

  std::cout << "ALL CHECKS PASSED\n";
  return 0;
}