// Module 12: 网关概念 — Middleware 链 / 玩具 JWT / Token Bucket（对应 gateway & auth）
#include <algorithm>
#include <cassert>
#include <chrono>
#include <cstdint>
#include <functional>
#include <iostream>
#include <string>
#include <vector>

static const char* B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
static std::string b64encode(const std::string& in) {
  std::string out; int val = 0, valb = -6;
  for (uint8_t c : in) { val = (val << 8) + c; valb += 8; while (valb >= 0) { out.push_back(B64[(val >> valb) & 63]); valb -= 6; } }
  if (valb > -6) out.push_back(B64[((val << 8) >> (valb + 8)) & 63]);
  while (out.size() % 4) out.push_back('=');
  return out;
}
static std::string toy_sign(const std::string& data, const std::string& secret) {
  uint32_t h = 0;
  for (size_t i = 0; i < data.size(); ++i)
    h = h * 131u + (uint8_t)data[i] + (uint8_t)secret[i % secret.size()];
  return b64encode(std::to_string(h));
}
static std::string IssueToken(const std::string& sub, const std::string& secret) {
  std::string body = b64encode("{\"alg\":\"TOY\"}") + "." + b64encode("{\"sub\":\"" + sub + "\"}");
  return body + "." + toy_sign(body, secret);
}
static bool VerifyToken(const std::string& token, const std::string& secret) {
  auto p2 = token.rfind('.');
  if (p2 == std::string::npos) return false;
  return token.substr(p2 + 1) == toy_sign(token.substr(0, p2), secret);
}

class TokenBucket {
 public:
  TokenBucket(double rate, double cap) : rate_(rate), cap_(cap), tokens_(cap), last_(Clock::now()) {}
  bool allow(double cost = 1.0) {
    refill();
    if (tokens_ < cost) return false;
    tokens_ -= cost; return true;
  }
  void set_last_ago_ms(int ms) { last_ = Clock::now() - std::chrono::milliseconds(ms); }
 private:
  using Clock = std::chrono::steady_clock;
  double rate_, cap_, tokens_; Clock::time_point last_;
  void refill() {
    auto now = Clock::now();
    tokens_ = std::min(cap_, tokens_ + std::chrono::duration<double>(now - last_).count() * rate_);
    last_ = now;
  }
};

struct Context { std::string path, token, body; int status = 200; bool aborted = false;
  void abort_with(int c, std::string m) { status = c; body = std::move(m); aborted = true; } };
using Handler = std::function<void(Context&)>;
using Middleware = std::function<void(Context&, const Handler&)>;
static Handler Chain(std::vector<Middleware> mws, Handler final_handler) {
  Handler h = std::move(final_handler);
  for (auto it = mws.rbegin(); it != mws.rend(); ++it) {
    Middleware mw = *it; Handler next = h;
    h = [mw, next](Context& ctx) { if (!ctx.aborted) mw(ctx, next); };
  }
  return h;
}

int main() {
  std::cout << "==== module12_microservices_concepts ====\n";
  const std::string secret = "gateway-secret";
  auto tok = IssueToken("alice", secret);
  assert(VerifyToken(tok, secret));
  assert(!VerifyToken(tok + "x", secret));
  std::cout << "[OK] toy JWT\n";

  TokenBucket b2(100.0, 1.0);
  assert(b2.allow()); assert(!b2.allow());
  b2.set_last_ago_ms(20);
  assert(b2.allow());
  std::cout << "[OK] token bucket\n";

  TokenBucket gw(1000.0, 5.0);
  auto Auth = [&](Context& ctx, const Handler& next) {
    if (!VerifyToken(ctx.token, secret)) { ctx.abort_with(401, "unauthorized"); return; }
    next(ctx);
  };
  auto RateLimit = [&](Context& ctx, const Handler& next) {
    if (!gw.allow()) { ctx.abort_with(429, "too many requests"); return; }
    next(ctx);
  };
  auto app = Chain({Auth, RateLimit}, [](Context& ctx) { ctx.body = "ok:" + ctx.path; });
  Context ok; ok.path = "/v1/kv"; ok.token = tok; app(ok);
  assert(ok.status == 200 && ok.body == "ok:/v1/kv");
  Context bad; bad.token = "bad"; app(bad);
  assert(bad.status == 401);
  std::cout << "[OK] middleware Auth->RateLimit->Handler\nALL CHECKS PASSED\n";
  return 0;
}