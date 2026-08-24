# SkyNet 设计文档

## 1. 定位

C++20 协程网络库 + **TitanKV 前置 HTTP 反向代理**（`skynet_gateway`）。

生产链路（`make run-all`）：

```
Client → skynet_gateway :8080 → Gin :18080 → Auth/Data/Meta/Observ/RAG
```

skynet **不做** JWT / 限流 / RBAC；读完完整 HTTP 请求后由 LoadBalancer 选 upstream（默认 Gin）。

## 2. 前置网关并发模型（最新）

`skynet/gateway/main.cpp`：

- **Main Reactor**（主线程）：1 个 `IOContext`，只跑 `acceptLoop` 协程
- **Sub Reactor**（`listen.threads`，gateway.yaml 默认 4）：每线程独立 `IOContext` + epoll
  - Main `accept` 后 round-robin 投递 client fd 到 Sub 的 pending 队列
  - Sub `spawn handleClient`，在本线程 `co_await` 读写（`io_awaitable.h`）
- epoll 模式：**ET**（`EPOLLET`）+ non-blocking + `watchOnce`（one-shot 风格挂起/恢复）
- **不是**「阻塞 IO 线程池」；协程挂起让出 CPU，Sub 继续 `poll` 其它 fd

配置见 `skynet/gateway/gateway.yaml`。冒烟：`make smoke-skynet`。

## 3. 库层组件

### 协程
- `Task<T>` / `detached_task`：promise 生命周期；`co_await` 链式调用
- `IoAwaitable`：fd 可读/可写时 resume

### 网络
- `Socket` + `asyncRead` / `asyncWriteAll`
- `Acceptor`：协程化 accept
- `IOContext`：epoll 事件环

### HTTP
- 增量状态机解析；支持 Content-Length 体；网关侧拼完整请求再转发

### 代理
- `LoadBalancer`：WRR / LeastConn / ConsistentHash
- `HealthCheck`：周期探测 upstream
- `ConnectionPool`：后端连接复用

## 4. 与 minikv_server 对照（面试常问）

| | minikv_server | skynet_gateway |
|--|--|--|
| 触发模式 | epoll **LT** | epoll **ET** |
| 并发原语 | 主从 Reactor + 业务线程池 | 主从 Reactor + **C++20 协程** |
| 默认职责 | KV TCP 协议 / LSM | HTTP 反代到 Gin |
| 默认端口 | 8888 | 8080 |
