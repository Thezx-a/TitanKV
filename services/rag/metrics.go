package rag

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// RagIngestTotal counts ingest outcomes: success|failed|queued|rejected.
	RagIngestTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "rag_ingest_total",
		Help: "RAG document ingest outcomes.",
	}, []string{"status"})

	// RagRetrieveDuration observes retrieve latency in seconds.
	RagRetrieveDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rag_retrieve_duration_seconds",
		Help:    "RAG retrieve latency.",
		Buckets: prometheus.DefBuckets,
	})

	// RagChatDuration observes chat end-to-end latency.
	RagChatDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "rag_chat_duration_seconds",
		Help:    "RAG chat latency.",
		Buckets: prometheus.DefBuckets,
	})

	// RagIndexSize is set on health / after ingest (optional gauge).
	RagIndexSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rag_index_size",
		Help: "In-memory vector index entry count.",
	})
)
