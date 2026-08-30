package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestCachedEmbedderExpiresByTTL(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	inner := NewHashEmbedder(8)
	c := NewCachedEmbedderTTL(inner, store, time.Hour) // 1h TTL

	_, err := c.Embed(context.Background(), "hello-ttl")
	if err != nil {
		t.Fatal(err)
	}

	hash := sha256.Sum256([]byte("hello-ttl"))
	key := embCacheKey(hex.EncodeToString(hash[:]))
	raw, ok, _ := store.Get(key)
	if !ok {
		t.Fatal("expected cache write")
	}
	var entry embCacheEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		var vec []float32
		if json.Unmarshal([]byte(raw), &vec) != nil {
			t.Fatalf("cache payload: %v", err)
		}
		entry = embCacheEntry{Vec: vec, CreatedAt: time.Now().Add(-2 * time.Hour).Unix()}
	} else {
		entry.CreatedAt = time.Now().Add(-2 * time.Hour).Unix()
	}
	buf, _ := json.Marshal(entry)
	_ = store.Put(key, string(buf))

	_, err = c.Embed(context.Background(), "hello-ttl")
	if err != nil {
		t.Fatal(err)
	}
	raw2, ok, _ := store.Get(key)
	if !ok {
		t.Fatal("cache should be rewritten after miss")
	}
	var entry2 embCacheEntry
	if err := json.Unmarshal([]byte(raw2), &entry2); err != nil {
		t.Fatal(err)
	}
	if time.Since(time.Unix(entry2.CreatedAt, 0)) > time.Minute {
		t.Fatalf("CreatedAt should be refreshed, got %v", entry2.CreatedAt)
	}
}

func TestPurgeExpiredEmbCache(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	key := embCacheKey("deadbeef")
	entry := embCacheEntry{Vec: []float32{1, 2}, CreatedAt: time.Now().Add(-48 * time.Hour).Unix()}
	buf, _ := json.Marshal(entry)
	_ = store.Put(key, string(buf))
	n, err := PurgeExpiredEmbCache(store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("want purged ≥1, got %d", n)
	}
	if _, ok, _ := store.Get(key); ok {
		t.Fatal("expired key should be deleted")
	}
}
