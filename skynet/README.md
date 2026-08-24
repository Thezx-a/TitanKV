<p align="center">
  <img src="https://img.shields.io/badge/C%2B%2B-20-00599C?style=for-the-badge&logo=cplusplus&logoColor=white" alt="C++20"/>
  <img src="https://img.shields.io/badge/Build-CMake-064F8C?style=for-the-badge&logo=cmake&logoColor=white" alt="CMake"/>
  <img src="https://img.shields.io/badge/Tests-9%20passing-brightgreen?style=for-the-badge" alt="Tests"/>
  <img src="https://img.shields.io/badge/License-MIT-blue?style=for-the-badge" alt="MIT"/>
  <img src="https://img.shields.io/badge/Coroutines-C%2B%2B20-purple?style=for-the-badge" alt="C++20"/>
</p>

<h1 align="center">🌐 SkyNet</h1>

<p align="center">
  <b>C++20 Coroutine Network Framework &amp; HTTP Reverse Proxy</b><br/>
  <i>Zero-cost abstractions · Event-driven · Production-grade</i>
</p>

---

## Overview

SkyNet is a **modern C++20 network framework** built on coroutines. It provides a high-level async I/O model where you write sequential-looking code that executes concurrently — like Go's goroutines but with C++'s zero-cost abstractions.

### Architecture

```mermaid
graph TB
    subgraph "Application"
        GW[HTTP Server / Reverse Proxy]
    end

    subgraph "SkyNet Framework"
        EXEC[Executor<br/>Task Scheduler]
        PARSE[HTTP/1.1 Parser]
        TIMER[TimerWheel<br/>Timeouts]
        LB[LoadBalancer<br/>Upstream Selection]
        HC[HealthChecker<br/>Periodic Probes]
        POOL[ConnectionPool<br/>Reuse]
    end

    subgraph "Network Layer"
        ACC[Acceptor<br/>Connection Accept]
        TCP[TcpStream<br/>Bidirectional]
        EP[Epoll Event Loop]
    end

    GW --> EXEC
    GW --> PARSE
    GW --> LB
    LB --> HC
    LB --> POOL
    EXEC --> TIMER
    EXEC --> ACC
    ACC --> TCP
    TCP --> EP

    style EXEC fill:#e1f5fe
    style EP fill:#e8f5e9
    style LB fill:#fff3e0
```

---


### TitanKV Front Proxy (`skynet_gateway`)

In the TitanKV monorepo, `skynet/gateway` is the **public :8080** reverse proxy in front of Gin `:18080`:

- epoll **edge-triggered** + C++20 coroutines (`io_awaitable.h`)
- Main reactor accepts; Sub reactors (`listen.threads`) run `handleClient` coroutines
- After a full HTTP request is read, LoadBalancer picks Gin (or other upstreams)
- JWT / rate-limit / RBAC stay in Gin — skynet only proxies

```bash
# from repo root
make run-skynet      # or included in make run-all
make smoke-skynet
```

## Coroutine Flow

```mermaid
sequenceDiagram
    participant Client
    participant Acceptor
    participant Executor
    participant Coroutine
    participant Socket

    Client->>Acceptor: TCP connect
    Acceptor->>Executor: spawn(handle_connection)
    
    loop Event Loop
        Executor->>Coroutine: resume()
        Coroutine->>Socket: co_await read()
        Note right of Coroutine: Suspends here
        Socket-->>Executor: data ready
        Executor->>Coroutine: resume()
        Coroutine->>Socket: co_await write()
        Note right of Coroutine: Suspends here
        Socket-->>Executor: write complete
        Executor->>Coroutine: resume()
        Coroutine-->>Executor: co_return
    end
```

---

## Coroutine vs Thread

```mermaid
graph LR
    subgraph "Thread Model (10K connections)"
        T1[Thread 1] 
        T2[Thread 2]
        T3[...]
        T10K[Thread 10K]
        style T1 fill:#ffcdd2
        style T10K fill:#ffcdd2
    end

    subgraph "SkyNet Model (10K connections)"
        E1[1 Event Loop Thread]
        C1[Coroutine 1<br/>~200B]
        C2[Coroutine 2<br/>~200B]
        C3[...<br/>~200B]
        C10K[Coroutine 10K<br/>~200B]
        style E1 fill:#c8e6c9
        style C1 fill:#e8f5e9
        style C10K fill:#e8f5e9
    end
```

| | Thread-per-connection | SkyNet Coroutines |
|---|---|---|
| 10K connections | 10K threads (crash) | 1 thread (smooth) |
| Memory per connection | ~1MB stack | ~200 bytes frame |
| Context switch | OS kernel (expensive) | User-space (cheap) |
| Blocking I/O | Thread stalls | Coroutine suspends |

---

## Quick Start

```bash
# Clone
git clone https://github.com/Thezx-a/SkyNet.git
cd SkyNet

# Build & Test
cmake -B build -G Ninja -DCMAKE_BUILD_TYPE=Release \
  -DENABLE_TESTS=ON -DCMAKE_CXX_COMPILER=g++-12
cmake --build build -j$(nproc)
ctest --test-dir build --output-on-failure
```

### Basic Coroutine Example

```cpp
#include <skynet/executor.h>
#include <skynet/task.h>
#include <skynet/tcp_socket.h>

skynet::Task<int> handle_client(skynet::TcpStream stream) {
    auto data = co_await stream.read(1024);  // pause until data arrives
    co_await stream.write("Hello!\n");       // pause until sent
    co_return 0;
}

int main() {
    skynet::Executor executor;
    skynet::Acceptor acceptor(executor, 8080);
    while (auto conn = co_await acceptor.accept()) {
        executor.spawn(handle_client(std::move(*conn)));
    }
}
```

---

## Component Map

```mermaid
classDiagram
    class Executor {
        +run()
        +spawn(Task)
        +schedule()
    }
    
    class Task~T~ {
        -coroutine_handle
        +resume()
        +done()
    }
    
    class Acceptor {
        +accept() Task~TcpStream~
        -socket
    }
    
    class TcpStream {
        +read(n) Task~bytes~
        +write(data) Task
        -fd
    }
    
    class TimerWheel {
        +add_timer(duration, cb)
        +cancel_timer(id)
        -ticks
    }
    
    class LoadBalancer {
        +select() Upstream
        +add(Upstream)
        +remove(Upstream)
    }
    
    Executor --> Task
    Executor --> Acceptor
    Acceptor --> TcpStream
    Executor --> TimerWheel
    LoadBalancer --> TcpStream
```

---

## Project Structure

```
SkyNet/
├── include/skynet/          Public API headers
│   ├── task.h               Coroutine Task type
│   ├── executor.h           Event loop & task scheduler
│   ├── tcp_socket.h         Coroutine TCP socket
│   ├── acceptor.h           Connection acceptor
│   ├── tcp_stream.h         Bidirectional TCP stream
│   ├── timer_wheel.h        Timer management
│   ├── upstream.h           Load balancer
│   └── http_parser.h        HTTP/1.1 parser
├── src/
│   ├── core/                Scheduler internals
│   ├── net/                 TCP layer
│   ├── http/                HTTP parser
│   └── proxy/               Reverse proxy logic
├── gateway/                 Gateway application
├── examples/                Example programs
├── tests/                   9 unit tests
└── docker/                  Docker deployment
```

---

## Tests

| Module | Tests | What's Verified |
|--------|-------|-----------------|
| Task | 2 | Basic await, exception propagation |
| Executor | 2 | Spawn, concurrent execution |
| TimerWheel | 2 | Add/remove, expiry |
| Upstream | 3 | Selection, health checks, removal |

---

## Use as a Library

```cmake
add_subdirectory(path/to/SkyNet)
target_link_libraries(your_app skynet)
```

---

## Tech Stack

`C++20` `Coroutines` `CMake` `Ninja` `Epoll` `Google Test` `Docker`

---

## License

[MIT](LICENSE)
