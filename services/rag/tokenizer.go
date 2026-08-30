package rag

import (
	"fmt"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// TokenEstimator estimates token counts for chunking (T2.9).
type TokenEstimator interface {
	Count(text string) int
	Name() string
}

// TokenEstimatorFunc adapts a function to TokenEstimator.
type TokenEstimatorFunc func(string) int

func (f TokenEstimatorFunc) Count(text string) int { return f(text) }
func (f TokenEstimatorFunc) Name() string         { return "func" }

// HeuristicTokenEstimator is the default UTF-8 density estimator (no deps).
type HeuristicTokenEstimator struct{}

func NewHeuristicTokenEstimator() TokenEstimator { return HeuristicTokenEstimator{} }

func (HeuristicTokenEstimator) Name() string { return "heuristic" }

func (HeuristicTokenEstimator) Count(s string) int { return estimateTokensHeuristic(s) }

// TiktokenEstimator wraps tiktoken-go (cl100k_base by default).
type TiktokenEstimator struct {
	enc  *tiktoken.Tiktoken
	name string
}

// NewTiktokenEstimator loads encoding (e.g. "cl100k_base"). May download vocab on first use.
func NewTiktokenEstimator(encoding string) (TokenEstimator, error) {
	if encoding == "" {
		encoding = "cl100k_base"
	}
	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, fmt.Errorf("tiktoken %s: %w", encoding, err)
	}
	return &TiktokenEstimator{enc: enc, name: "tiktoken:" + encoding}, nil
}

func (t *TiktokenEstimator) Name() string { return t.name }

func (t *TiktokenEstimator) Count(text string) int {
	if text == "" {
		return 0
	}
	n := len(t.enc.Encode(text, nil, nil))
	if n < 1 {
		return 1
	}
	return n
}

var (
	defaultEstimatorMu sync.RWMutex
	defaultEstimator   TokenEstimator = NewHeuristicTokenEstimator()
	tokenizerMode      = "heuristic"
)

// SetDefaultTokenEstimator sets the package-wide estimator used by Chunker when unset.
func SetDefaultTokenEstimator(e TokenEstimator, mode string) {
	defaultEstimatorMu.Lock()
	defer defaultEstimatorMu.Unlock()
	if e == nil {
		e = NewHeuristicTokenEstimator()
		mode = "heuristic"
	}
	defaultEstimator = e
	if mode == "" {
		mode = e.Name()
	}
	tokenizerMode = mode
}

func getDefaultTokenEstimator() TokenEstimator {
	defaultEstimatorMu.RLock()
	defer defaultEstimatorMu.RUnlock()
	return defaultEstimator
}

// ActiveChunkerVersion returns DocumentMeta.chunker_version for the tokenizer mode.
func ActiveChunkerVersion(mode string) string {
	switch mode {
	case "tiktoken", "cl100k_base":
		return "v3-tiktoken"
	default:
		return ChunkerVersion // v2 heuristic
	}
}

// InitTokenizerFromConfig sets default estimator from RAG_TOKENIZER env (heuristic|tiktoken).
func InitTokenizerFromConfig(mode, encoding string) error {
	switch mode {
	case "", "heuristic", "approx":
		SetDefaultTokenEstimator(NewHeuristicTokenEstimator(), "heuristic")
		return nil
	case "tiktoken", "cl100k_base":
		if encoding == "" {
			encoding = "cl100k_base"
		}
		e, err := NewTiktokenEstimator(encoding)
		if err != nil {
			return err
		}
		SetDefaultTokenEstimator(e, "tiktoken")
		return nil
	default:
		return fmt.Errorf("unknown RAG_TOKENIZER %q (want heuristic|tiktoken)", mode)
	}
}

// CurrentTokenizerMode returns heuristic|tiktoken.
func CurrentTokenizerMode() string {
	defaultEstimatorMu.RLock()
	defer defaultEstimatorMu.RUnlock()
	return tokenizerMode
}
