package titan

// KVPair 单条 key-value.
type KVPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Metrics 控制台仪表盘指标 (与 observability service 一致).
type Metrics struct {
	QPS           float64 `json:"qps"`
	P50LatencyMs  float64 `json:"p50_ms"`
	P99LatencyMs  float64 `json:"p99_ms"`
	StorageGB     float64 `json:"storage_gb"`
	StorageKnown  bool    `json:"storage_known"`
	NodeCount     int     `json:"node_count"`
	LeaderCount   int     `json:"leader_count"`
	Timestamp     int64   `json:"timestamp"`
	QPSSource     string  `json:"qps_source"`
	LatencyApprox bool    `json:"latency_approx"`
	DataBackend   string  `json:"data_backend"` // minikv | memory | unknown
}

// Collection 元数据.
type Collection struct {
	Name      string            `json:"name"`
	TTL       int               `json:"ttl_seconds"`
	Schema    map[string]string `json:"schema"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

// User 用户 (用户管理界面用).
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// LoginResponse 登录响应.
type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	UserID       string `json:"user_id"`
	Role         string `json:"role"`
}
