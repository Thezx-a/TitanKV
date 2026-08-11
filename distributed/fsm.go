package distributed

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/raft"
)

// KVFSM is an in-memory map FSM. Raft Apply is the source of truth.
// Optional WriteForwarder mirrors Put/Delete to MiniKV best-effort and must
// not fail the Apply (external I/O is non-deterministic).
type KVFSM struct {
	mu        sync.RWMutex
	data      map[string]string
	forwarder WriteForwarder
}

// NewKVFSM creates an empty FSM. forwarder may be nil.
func NewKVFSM(forwarder WriteForwarder) *KVFSM {
	return &KVFSM{
		data:      make(map[string]string),
		forwarder: forwarder,
	}
}

// Apply implements raft.FSM.
func (f *KVFSM) Apply(l *raft.Log) interface{} {
	cmd, err := decodeCommand(l.Data)
	if err != nil {
		return fmt.Errorf("decode command: %w", err)
	}
	return f.applyLocal(cmd)
}

func (f *KVFSM) applyLocal(cmd *command) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd.Op {
	case opPut:
		f.data[cmd.Key] = cmd.Value
		if f.forwarder != nil {
			_ = f.forwarder.Put(cmd.Key, cmd.Value) // best-effort
		}
	case opDelete:
		delete(f.data, cmd.Key)
		if f.forwarder != nil {
			_ = f.forwarder.Delete(cmd.Key) // best-effort
		}
	default:
		return fmt.Errorf("unknown op %q", cmd.Op)
	}
	return nil
}

// Get reads from the local FSM map (not via Raft).
func (f *KVFSM) Get(key string) (string, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	v, ok := f.data[key]
	return v, ok
}

// Snapshot implements raft.FSM — JSON object of the whole map.
func (f *KVFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	clone := make(map[string]string, len(f.data))
	for k, v := range f.data {
		clone[k] = v
	}
	return &kvSnapshot{data: clone}, nil
}

// Restore implements raft.FSM.
func (f *KVFSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var data map[string]string
	if err := json.NewDecoder(rc).Decode(&data); err != nil {
		return err
	}
	if data == nil {
		data = make(map[string]string)
	}
	f.mu.Lock()
	f.data = data
	f.mu.Unlock()
	return nil
}

type kvSnapshot struct {
	data map[string]string
}

func (s *kvSnapshot) Persist(sink raft.SnapshotSink) error {
	err := func() error {
		b, err := json.Marshal(s.data)
		if err != nil {
			return err
		}
		if _, err := sink.Write(b); err != nil {
			return err
		}
		return sink.Close()
	}()
	if err != nil {
		_ = sink.Cancel()
	}
	return err
}

func (s *kvSnapshot) Release() {}
