// TitanKV Raft teaching node.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/titan-kv/titan/distributed"
	"github.com/titan-kv/titan/pkg/metrics"
)

func main() {
	bind := getenv("RAFT_BIND", "127.0.0.1:8090")
	httpAddr := getenv("RAFT_HTTP", ":8091")
	nodeID := getenv("RAFT_NODE_ID", "node1")

	node, err := distributed.Bootstrap(distributed.Config{
		NodeID:     nodeID,
		DataDir:    getenv("RAFT_DATA_DIR", "./data/raft_"+nodeID),
		BindAddr:   bind,
		MiniKVAddr: os.Getenv("MINIKV_ADDR"),
	})
	if err != nil {
		log.Fatalf("raft bootstrap: %v", err)
	}
	defer node.Shutdown()

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.HTTPHandler())
	mux.Handle("/", node.Handler())

	srv := &http.Server{Addr: httpAddr, Handler: mux}
	go func() {
		log.Printf("[INFO] raft node %s bind=%s http=%s", nodeID, bind, httpAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
