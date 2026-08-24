package distributed

import (
	"fmt"
	"os"
	"time"

	"github.com/hashicorp/raft"
)

// Config bootstraps a teaching single-voter Raft node.
type Config struct {
	NodeID  string // raft.ServerID, e.g. "node1"
	DataDir string // snapshots (+ reserved for future durable stores)
	// BindAddr is host:port for Raft TCP. Use "127.0.0.1:0" for tests.
	BindAddr string
	// MiniKVAddr optionally forwards FSM writes. Empty → os.Getenv("MINIKV_ADDR").
	MiniKVAddr string
	// ApplyTimeout bounds Raft Apply futures (default 5s).
	ApplyTimeout time.Duration
}

// Node is a 1-node Raft replica with an in-memory KV FSM.
type Node struct {
	raft   *raft.Raft
	fsm    *KVFSM
	fwd    WriteForwarder
	timeout time.Duration
}

// Bootstrap creates stores/transport, starts Raft, and bootstraps a single voter
// when the cluster has no configuration yet.
func Bootstrap(cfg Config) (*Node, error) {
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("NodeID required")
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("DataDir required")
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1:0"
	}
	if cfg.ApplyTimeout <= 0 {
		cfg.ApplyTimeout = 5 * time.Second
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}

	miniAddr := cfg.MiniKVAddr
	if miniAddr == "" {
		miniAddr = os.Getenv("MINIKV_ADDR")
	}
	var fwd WriteForwarder
	if miniAddr != "" {
		fwd = newMiniKVForwarder(miniAddr)
	}
	fsm := NewKVFSM(fwd)

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)
	// Faster elections in tests / teaching demos.
	rc.HeartbeatTimeout = 100 * time.Millisecond
	rc.ElectionTimeout = 100 * time.Millisecond
	rc.LeaderLeaseTimeout = 50 * time.Millisecond
	rc.CommitTimeout = 20 * time.Millisecond

	// Teaching default: in-memory log/stable (not crash-durable across process).
	logStore := raft.NewInmemStore()
	stableStore := raft.NewInmemStore()
	snapshots, err := raft.NewFileSnapshotStore(cfg.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("snapshot store: %w", err)
	}

	// advertise=nil: TCPTransport uses the real bound LocalAddr (needed for :0).
	transport, err := raft.NewTCPTransport(cfg.BindAddr, nil, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("raft transport: %w", err)
	}

	r, err := raft.NewRaft(rc, fsm, logStore, stableStore, snapshots, transport)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("new raft: %w", err)
	}

	// Bootstrap only when there is no existing configuration (fresh DataDir).
	hasState, err := raft.HasExistingState(logStore, stableStore, snapshots)
	if err != nil {
		_ = r.Shutdown().Error()
		return nil, err
	}
	if !hasState {
		configuration := raft.Configuration{
			Servers: []raft.Server{{
				Suffrage: raft.Voter,
				ID:       raft.ServerID(cfg.NodeID),
				Address:  transport.LocalAddr(),
			}},
		}
		f := r.BootstrapCluster(configuration)
		if err := f.Error(); err != nil {
			_ = r.Shutdown().Error()
			return nil, fmt.Errorf("bootstrap: %w", err)
		}
	}

	return &Node{raft: r, fsm: fsm, fwd: fwd, timeout: cfg.ApplyTimeout}, nil
}

// WaitForLeader blocks until this node is leader or timeout.
func (n *Node) WaitForLeader(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.IsLeader() {
			return nil
		}
		// Also accept observing a leader address (1-node: ourselves).
		addr, lid := n.raft.LeaderWithID()
		if addr != "" && lid != "" {
			if n.IsLeader() {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for leader (state=%s)", n.raft.State())
}

// IsLeader reports whether this node holds leadership.
func (n *Node) IsLeader() bool {
	return n.raft.State() == raft.Leader
}

// Put replicates a put through Raft Apply.
func (n *Node) Put(key, value string) error {
	return n.apply(&command{Op: opPut, Key: key, Value: value})
}

// Delete replicates a delete through Raft Apply.
func (n *Node) Delete(key string) error {
	return n.apply(&command{Op: opDelete, Key: key})
}

// Get reads the local FSM (linearizable only on leader after Apply ack).
func (n *Node) Get(key string) (string, bool) {
	return n.fsm.Get(key)
}

func (n *Node) apply(cmd *command) error {
	if !n.IsLeader() {
		return fmt.Errorf("not leader")
	}
	b, err := encodeCommand(cmd)
	if err != nil {
		return err
	}
	future := n.raft.Apply(b, n.timeout)
	if err := future.Error(); err != nil {
		return err
	}
	if resp := future.Response(); resp != nil {
		if e, ok := resp.(error); ok && e != nil {
			return e
		}
	}
	return nil
}

// Raft exposes the underlying raft.Raft for advanced callers/tests.
func (n *Node) Raft() *raft.Raft { return n.raft }

// Members returns current Raft configuration (for HTTP /cluster API).
func (n *Node) Members() ([]map[string]string, error) {
	f := n.raft.GetConfiguration()
	if err := f.Error(); err != nil {
		return nil, err
	}
	cfg := f.Configuration()
	out := make([]map[string]string, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		role := "voter"
		if s.Suffrage == raft.Nonvoter {
			role = "nonvoter"
		}
		out = append(out, map[string]string{
			"id":      string(s.ID),
			"address": string(s.Address),
			"role":    role,
		})
	}
	return out, nil
}

// FSM exposes the KV FSM.
func (n *Node) FSM() *KVFSM { return n.fsm }

// Shutdown stops Raft and closes optional MiniKV forwarder.
func (n *Node) Shutdown() error {
	future := n.raft.Shutdown()
	err := future.Error()
	if n.fwd != nil {
		_ = n.fwd.Close()
	}
	return err
}
