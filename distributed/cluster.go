package distributed

import (
	"fmt"
	"time"

	"github.com/hashicorp/raft"
)

// ClusterConfig bootstraps a multi-voter Raft cluster (teaching/demo).
type ClusterConfig struct {
	NodeID   string
	Peers    []string // host:port of other nodes (this node's addr is BindAddr)
	DataDir  string
	BindAddr string
}

// JoinCluster starts Raft and joins peers when Peers is non-empty.
func JoinCluster(cfg ClusterConfig) (*Node, error) {
	if cfg.NodeID == "" || cfg.DataDir == "" {
		return nil, fmt.Errorf("NodeID and DataDir required")
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1:0"
	}
	n, err := Bootstrap(Config{
		NodeID:       cfg.NodeID,
		DataDir:      cfg.DataDir,
		BindAddr:     cfg.BindAddr,
		ApplyTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if len(cfg.Peers) == 0 {
		return n, nil
	}
	// Teaching join: add voters for peer IDs derived from host order.
	for i, peer := range cfg.Peers {
		id := raft.ServerID(fmt.Sprintf("node%d", i+2))
		f := n.raft.AddVoter(id, raft.ServerAddress(peer), 0, 0)
		if err := f.Error(); err != nil {
			return nil, fmt.Errorf("add voter %s: %w", peer, err)
		}
	}
	return n, nil
}
