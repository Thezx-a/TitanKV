// Package gateway 是 TitanKV 的 API 网关入口 (Phase 3).
//
// 中间件链 (洋葱模型):
//   RequestID → Logger → Recover → RateLimit → Auth → RBAC → Handler
//
// 启动:
//   go run ./cmd/gateway
//
// 路由:
//   GET  /ping              - 健康检查 (无鉴权)
//   GET  /healthz           - K8s probe
//   POST /api/auth/register - 注册
//   POST /api/auth/login    - 登录
//   POST /api/auth/refresh  - 刷新 token
//   POST /api/auth/logout   - 登出 (吊销 jti)
//   POST /api/auth/apikey   - 签发 API Key (需 apikey:issue 权限)
//
//   /api/data/*  → 反向代理到 data-service (Phase 4)
//   /api/meta/*  → 反向代理到 meta-service (Phase 4)
//   /api/observability/* → 反向代理到 observability-service (Phase 4)
//   /api/rag/*   → 反向代理到 rag-service (Phase 5, RBAC: rag:ingest / rag:query)
package gateway

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/titan-kv/titan/gateway/handler"
	"github.com/titan-kv/titan/gateway/middleware"
	"github.com/titan-kv/titan/pkg/metrics"
	"github.com/titan-kv/titan/services/auth"
)

// Config Gateway 配置, 从环境变量读取.
type Config struct {
	Addr            string
	JWTSecret       string
	RedisAddr       string
	AuthServiceURL  string
	DataServiceURL  string
	MetaServiceURL  string
	ObservURL       string
	RAGServiceURL   string
}

func loadConfig() Config {
	return Config{
		Addr:            getenv("GATEWAY_ADDR", ":8080"),
		JWTSecret:       getenv("JWT_SECRET", "dev-secret-change-in-production"),
		RedisAddr:       getenv("REDIS_ADDR", "localhost:6379"),
		AuthServiceURL:  getenv("AUTH_SERVICE_URL", "http://localhost:8082"),
		DataServiceURL:  getenv("DATA_SERVICE_URL", "http://localhost:8081"),
		MetaServiceURL:  getenv("META_SERVICE_URL", "http://localhost:8083"),
		ObservURL:       getenv("OBSERV_SERVICE_URL", "http://localhost:8084"),
		RAGServiceURL:   getenv("RAG_SERVICE_URL", "http://localhost:8085"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Run 启动 Gateway. 阻塞调用, 直到收到 SIGTERM/SIGINT.
func Run() {
	cfg := loadConfig()

	// Redis 客户端 (限流 + jti 黑名单)
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("[WARN] redis not available: %v (rate-limit degraded to no-op)", err)
		cancel()
		rdb = nil
	} else {
		cancel()
	}

	// Auth 服务的客户端 (Gateway 转发 /api/auth/* 给 Auth 服务)
	authClient := auth.NewClient(cfg.AuthServiceURL)

	// API Key 验证回调 (调用 Auth 服务)
	apiKeyVerify := func(ctx context.Context, key string) (userID, role string, ok bool) {
		return authClient.VerifyAPIKey(ctx, key)
	}

	r := gin.New()
	r.Use(gin.Recovery()) // gin 内置兜底, 我们自己的 Recover 在外层
	r.Use(metrics.GinMiddleware("gateway"))
	metrics.RegisterRoutes(r)
	r.Use(
		middleware.RequestID(),
		middleware.Logger(),
		middleware.Recover(),
	)
	if rdb != nil {
		r.Use(middleware.RateLimit(rdb, 100, 10)) // 100 burst, 10/s sustained
	}

	// 健康检查 (无鉴权)；对 data/rag 做真实 probe（Phase O）
	r.GET("/ping", handler.Ping)
	r.GET("/healthz", handler.Healthz("0.1.0", handler.HealthProbes{
		Targets: map[string]string{
			"data": cfg.DataServiceURL + "/healthz",
			"rag":  cfg.RAGServiceURL + "/healthz",
			"auth": cfg.AuthServiceURL + "/healthz",
		},
	}))
	RegisterClusterRoutes(r)

	// Auth 路由 (无鉴权)
	r.POST("/api/auth/register", authClient.RegisterHandler)
	r.POST("/api/auth/login", authClient.LoginHandler)
	// Auth 路由 (需鉴权)
	authGrp := r.Group("/api/auth",
		middleware.Auth(middleware.AuthConfig{JWTSecret: cfg.JWTSecret, Redis: rdb, APIKeyVerify: apiKeyVerify}))
	authGrp.POST("/refresh", authClient.RefreshHandler)
	authGrp.POST("/logout", authClient.LogoutHandler)
	authGrp.POST("/apikey",
		middleware.RBAC(middleware.PermAPIKeyIssue),
		authClient.IssueAPIKeyHandler)

	// 业务路由 (需鉴权 + RBAC + 反向代理)
	// 注意: 不能用 /api/* 通配, 会与上面的 /api/auth 冲突 (Gin 路由树限制).
	rp, err := handler.NewReverseProxy(map[string]string{
		"/api/data":          cfg.DataServiceURL,
		"/api/meta":          cfg.MetaServiceURL,
		"/api/observability": cfg.ObservURL,
		"/api/rag":           cfg.RAGServiceURL,
	})
	if err != nil {
		log.Fatalf("new reverse proxy: %v", err)
	}

	authMW := middleware.Auth(middleware.AuthConfig{
		JWTSecret:    cfg.JWTSecret,
		Redis:        rdb,
		APIKeyVerify: apiKeyVerify,
	})

	// /api/data, /api/meta, /api/observability: 按 HTTP 方法映射 KV/Collection 权限.
	kvRBAC := func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet:
			middleware.RBAC(middleware.PermKVGet)(c)
		case http.MethodPost, http.MethodPut:
			middleware.RBAC(middleware.PermKVPut)(c)
		case http.MethodDelete:
			middleware.RBAC(middleware.PermKVDelete)(c)
		}
		if c.IsAborted() {
			return
		}
		c.Next()
	}

	for _, prefix := range []string{"/api/data", "/api/meta", "/api/observability"} {
		g := r.Group(prefix, authMW, kvRBAC)
		g.Any("/*proxyPath", rp.Handle)
		g.Any("", rp.Handle)
	}

	// /api/rag: 区分 rag:ingest (写) / rag:query (读 + 检索 + 问答).
	// retrieve 与 chat 虽是 POST 但属 query 权限.
	ragRBAC := func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case strings.HasSuffix(path, "/retrieve") || strings.HasSuffix(path, "/chat"):
			middleware.RBAC(middleware.PermRAGQuery)(c)
		case c.Request.Method == http.MethodGet ||
			c.Request.Method == http.MethodHead ||
			c.Request.Method == http.MethodOptions:
			middleware.RBAC(middleware.PermRAGQuery)(c)
		case c.Request.Method == http.MethodPost ||
			c.Request.Method == http.MethodPut ||
			c.Request.Method == http.MethodPatch ||
			c.Request.Method == http.MethodDelete:
			middleware.RBAC(middleware.PermRAGIngest)(c)
		default:
			middleware.RBAC(middleware.PermRAGQuery)(c)
		}
		if c.IsAborted() {
			return
		}
		c.Next()
	}

	ragGrp := r.Group("/api/rag", authMW, ragRBAC)
	ragGrp.Any("/*proxyPath", rp.Handle)
	ragGrp.Any("", rp.Handle)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: r,
	}

	go func() {
		log.Printf("[INFO] gateway listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// 优雅停机
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[INFO] gateway shutting down...")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := srv.Shutdown(ctx2); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
}
