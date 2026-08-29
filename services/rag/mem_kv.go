package rag

import (
	"sort"
	"sync"

	"github.com/titan-kv/titan/services/data"
)

// MemKV is an in-memory kvClient for unit tests (no minikv required).
type MemKV struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewMemKV constructs an empty in-memory KV.
func NewMemKV() *MemKV {
	return &MemKV{m: make(map[string]string)}
}

func (m *MemKV) Close() error { return nil }

func (m *MemKV) Put(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = value
	return nil
}

func (m *MemKV) Get(key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.m[key]
	return v, ok, nil
}

func (m *MemKV) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key)
	return nil
}

func (m *MemKV) DeleteRange(start, end string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.m {
		if k >= start && k < end {
			delete(m.m, k)
		}
	}
	return nil
}

func (m *MemKV) Scan(start, end string) ([]data.KVPair, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0)
	for k := range m.m {
		if k >= start && k < end {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]data.KVPair, 0, len(keys))
	for _, k := range keys {
		out = append(out, data.KVPair{Key: k, Value: m.m[k]})
	}
	return out, nil
}

func (m *MemKV) WriteBatch(ops []data.BatchOp) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range ops {
		if op.Put {
			m.m[op.Key] = op.Value
		} else {
			delete(m.m, op.Key)
		}
	}
	return nil
}
