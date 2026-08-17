# TitanKV — WSL Ubuntu 22.04 运行指南

本指南保证在 **Windows + WSL2 + Ubuntu 22.04** 上可以从零：克隆 → 编译 → 跑通冒烟。

仓库地址：`https://github.com/Thezx-a/TitanKV`

---

## 0. 环境要求

| 组件 | 版本 |
|------|------|
| WSL2 | Ubuntu **22.04** |
| CMake | ≥ 3.20 |
| 编译器 | **g++-12**（C++17 MiniKV + C++20 SkyNet） |
| Ninja | 推荐 |
| Go | **1.23+**（Ubuntu 默认 apt 过旧，需单独安装） |
| Node.js | **20+**（跑 Web 控制台时需要） |
| Docker | 可选（`make docker-up` 起 Redis/Prometheus 等） |

---

## 1. 进入 WSL

```bash
wsl -d Ubuntu-22.04
```

**不要在 `/mnt/c/...` 下长时间编译。** 把代码放到 Linux 家目录：

```bash
cd ~
git clone https://github.com/Thezx-a/TitanKV.git titan-kv
cd titan-kv
```

若你已在 Windows 桌面改代码，可用：

```bash
rsync -a --delete \
  --exclude 'build' --exclude 'build-*' --exclude 'web/node_modules' --exclude 'web/.next' \
  /mnt/c/Users/Administrator/Desktop/hellocpp/ ~/titan-kv/
cd ~/titan-kv
```

---

## 2. 安装系统依赖

```bash
sudo apt update
sudo apt install -y build-essential g++-12 cmake ninja-build git curl pkg-config
```

### 安装 Go 1.23+

```bash
curl -fsSL https://go.dev/dl/go1.23.6.linux-amd64.tar.gz -o /tmp/go.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc
source ~/.bashrc
go version
```

### （可选）安装 Node 20

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
node -v
```

---

## 3. 编译 C++（MiniKV + SkyNet）

```bash
cd ~/titan-kv

cmake -B build -G Ninja \
  -DCMAKE_BUILD_TYPE=Release \
  -DENABLE_TESTS=ON \
  -DCMAKE_CXX_COMPILER=g++-12 \
  -DCMAKE_POLICY_VERSION_MINIMUM=3.5

cmake --build build -j$(nproc)
```

成功后应有：

```bash
ls -lh build/minikv/libminikv.a
ls -lh build/minikv/minikv_server
ls -lh build/skynet/libskynet.a
ls -lh build/skynet/skynet_gateway
```

跑测试：

```bash
ctest --test-dir build --output-on-failure --parallel $(nproc)
```

或使用 Makefile：

```bash
make cmake-build
make cpp-test
```

---

## 4. 编译 / 跑 Go 服务

```bash
cd ~/titan-kv
go mod tidy
go build ./...
```

一键起 5 个服务（auth/data/meta/observability/gateway）：

```bash
make run-all
```

冒烟：

```bash
curl -s http://127.0.0.1:8080/ping

> `make run-all` 现在：对外 **:8080 = skynet 前置反向代理**（半包拼包 / 线程池 / SSE 流式 / 优雅退出）→ Gin `:18080` → Data/Auth…。单独跑 Gin 仍是 `make run-gateway`（默认 :8080）。验证前置：`make smoke-skynet`。
```

| 服务 | 端口 |
|------|------|
| Gateway（对外 skynet 前置） | 8080 |
| Gin（内网，run-all 时） | 18080 |
| Data | 8081 |
| Auth | 8082 |
| Meta | 8083 |
| Observability | 8084 |

---

## 5. （可选）Web 控制台

```bash
make web-install
make web-dev
```

浏览器打开：http://localhost:3000

---

## 6. （可选）依赖栈 Docker

```bash
make docker-up
```

启动 Postgres / Redis / etcd / Jaeger / Prometheus / Grafana（Grafana 映射到 **3001**，避免和 Next.js 抢 3000）。

---

## 7. 模块说明

| 目录 | 说明 | 构建 |
|------|------|------|
| `minikv/` | C++17 LSM-Tree 引擎 | 根 CMake / 独立 `cmake -S minikv` |
| `skynet/` | C++20 协程网络库 | 根 CMake / 独立 `cmake -S skynet` |
| `cmd/*` | Go 服务入口 | `go run ./cmd/gateway` 等 |
| `gateway/` | Go Gin 网关库 | 由 `cmd/gateway` 引用 |
| `services/*` | Auth / Data / Meta / Observ 库 | 由 `cmd/*` 引用 |
| `web/` | Next.js 控制台 | `npm run dev` |
| `deploy/dev/` | 本地依赖 compose | `make docker-up` |
| `docs/course/` | 双语课程 | 阅读即可 |

---

## 8. 常见问题

| 现象 | 处理 |
|------|------|
| `/mnt/c` 编译极慢 | 代码放到 `~/titan-kv` |
| `g++` 版本过低 | 用 `g++-12`：`-DCMAKE_CXX_COMPILER=g++-12` |
| Go 版本过低 | 不要用 apt 的 go 1.18，按上面装官方 1.23 |
| FetchContent 下 snappy 失败 | 检查网络；加 `-DCMAKE_POLICY_VERSION_MINIMUM=3.5` |
| `make run-all` 端口占用 | `ss -lptn 'sport = :8080'` 查占用进程并结束 |
| SkyNet 在原生 Windows 编不过 | 必须用 WSL/Linux（epoll） |

---

## 9. 每天启动（最短）

```bash
cd ~/titan-kv
make run-all          # 终端 1
make web-dev          # 终端 2（可选）
```

C++ 引擎单独验证：

```bash
./build/minikv/minikv_server --host 0.0.0.0 --port 8888 --io-threads 4 --biz-threads 4
```
