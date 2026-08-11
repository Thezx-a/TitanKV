package data

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// KVStore is the persistence backend for the data service.
type KVStore interface {
	Put(key, value string) error
	Get(key string) (string, bool, error)
	Delete(key string) error
	Scan(start, end string) ([]KVPair, error)
	Size() int
	Backend() string
	Close() error
}

// Store is the in-memory KV storage (MVP fallback).
type Store struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewStore creates an in-memory KV store.
func NewStore() *Store {
	return &Store{data: make(map[string]string)}
}

func (s *Store) Put(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *Store) Get(key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok, nil
}

func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// KVPair is a single key-value (aligned with client-go/titan/types.go).
type KVPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Scan returns keys in [start, end) half-open interval.
func (s *Store) Scan(start, end string) ([]KVPair, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]KVPair, 0, len(s.data))
	for k, v := range s.data {
		if start != "" && k < start {
			continue
		}
		if end != "" && k >= end {
			continue
		}
		out = append(out, KVPair{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].Key, out[j].Key) < 0
	})
	return out, nil
}

func (s *Store) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

func (s *Store) Backend() string { return "memory" }

func (s *Store) Close() error { return nil }

// MiniKVStore persists via C++ minikv_server (TCP binary protocol).
type MiniKVStore struct {
	client *MiniKVClient
}

func NewMiniKVStore(addr string) *MiniKVStore {
	return &MiniKVStore{client: NewMiniKVClient(addr)}
}

func (s *MiniKVStore) Put(key, value string) error {
	return s.client.Put(key, value)
}

func (s *MiniKVStore) Get(key string) (string, bool, error) {
	return s.client.Get(key)
}

func (s *MiniKVStore) Delete(key string) error {
	return s.client.Delete(key)
}

func (s *MiniKVStore) Scan(start, end string) ([]KVPair, error) {
	return s.client.Scan(start, end)
}

func (s *MiniKVStore) Size() int {
	// Engine has no cheap COUNT; healthz uses Backend() instead.
	return -1
}

func (s *MiniKVStore) Backend() string { return "minikv" }

func (s *MiniKVStore) Close() error { return s.client.Close() }

// NewStoreFromEnv picks minikv when MINIKV_ADDR is set, else memory.
// Example: MINIKV_ADDR=127.0.0.1:8888
func NewStoreFromEnv() (KVStore, error) {
	addr := os.Getenv("MINIKV_ADDR")
	if addr == "" {
		return NewStore(), nil
	}
	store := NewMiniKVStore(addr)
	// Probe with a non-existent get to ensure the server is reachable.
	_, _, err := store.Get("__titankv_health_probe__")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("minikv unreachable at %s: %w", addr, err)
	}
	return store, nil
}
