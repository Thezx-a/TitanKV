package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// RebuildFromStore re-embeds every rag:chunk:* record into idx (M7).
// Used when snapshots are missing/corrupt so retrieve stays consistent with minikv.
func RebuildFromStore(ctx context.Context, store *Store, emb Embedder, idx VectorIndex) (int, error) {
	if store == nil || emb == nil || idx == nil {
		return 0, fmt.Errorf("rebuild: nil dependency")
	}
	start, end := prefixRange("rag:chunk:")
	pairs, err := store.Scan(start, end)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pairs {
		var c ChunkRecord
		if err := json.Unmarshal([]byte(p.Value), &c); err != nil {
			continue
		}
		if c.Col == "" || c.DocID == "" {
			continue
		}
		vec, err := emb.Embed(ctx, c.Text)
		if err != nil {
			return n, fmt.Errorf("embed %s: %w", c.ChunkID, err)
		}
		cid := chunkID(c.Col, c.DocID, c.Seq)
		idx.Add(cid, vec)
		n++
	}
	return n, nil
}

// maybeRebuildIfEmpty rebuilds when the in-memory index is empty but store has chunks.
func maybeRebuildIfEmpty(ctx context.Context, store *Store, emb Embedder, idx VectorIndex) {
	if idx.Size() > 0 {
		return
	}
	n, err := RebuildFromStore(ctx, store, emb, idx)
	if err != nil {
		log.Printf("[rag] RebuildFromStore failed: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[rag] RebuildFromStore restored %d vectors (snapshots missing/empty)", n)
	}
}
