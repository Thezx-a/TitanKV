package distributed

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOneNodeElectsLeaderAndPutGet exercises the teaching 1-node Raft bootstrap:
// single voter becomes leader, Apply(Put) is readable via Get.
func TestOneNodeElectsLeaderAndPutGet(t *testing.T) {
	dir := t.TempDir()
	n, err := Bootstrap(Config{
		NodeID:   "node1",
		DataDir:  filepath.Join(dir, "raft"),
		BindAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = n.Shutdown() }()

	if err := n.WaitForLeader(5 * time.Second); err != nil {
		t.Fatalf("WaitForLeader: %v", err)
	}
	if !n.IsLeader() {
		t.Fatal("expected node to be leader after WaitForLeader")
	}

	if err := n.Put("hello", "world"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	v, ok := n.Get("hello")
	if !ok || v != "world" {
		t.Fatalf("Get: got (%q, %v), want world/true", v, ok)
	}

	if err := n.Delete("hello"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := n.Get("hello"); ok {
		t.Fatal("Get after Delete: key should be gone")
	}
}

func TestFSMSnapshotRestore(t *testing.T) {
	fsm := NewKVFSM(nil)
	_ = fsm.applyLocal(&command{Op: opPut, Key: "a", Value: "1"})
	_ = fsm.applyLocal(&command{Op: opPut, Key: "b", Value: "2"})

	snap, err := fsm.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	path := filepath.Join(t.TempDir(), "snap.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Persist(&fileSink{f: f}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	_ = f.Close()

	raw, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	fsm2 := NewKVFSM(nil)
	if err := fsm2.Restore(raw); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if v, ok := fsm2.Get("a"); !ok || v != "1" {
		t.Fatalf("restored a: (%q,%v)", v, ok)
	}
	if v, ok := fsm2.Get("b"); !ok || v != "2" {
		t.Fatalf("restored b: (%q,%v)", v, ok)
	}
}

// fileSink adapts *os.File to raft.SnapshotSink for unit tests.
type fileSink struct{ f *os.File }

func (s *fileSink) Write(p []byte) (int, error) { return s.f.Write(p) }
func (s *fileSink) Close() error                { return s.f.Close() }
func (s *fileSink) ID() string                  { return "test" }
func (s *fileSink) Cancel() error               { return s.f.Close() }
