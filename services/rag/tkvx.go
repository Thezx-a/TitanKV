package rag

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"sort"
)

// ErrCorruptSnapshot is returned when a TKVX file fails CRC or structure checks.
var ErrCorruptSnapshot = errors.New("corrupt TKVX snapshot")

const (
	tkvxMagic    = "TKVX"
	tkvxVersion1 = uint16(1) // vectors only
	tkvxVersion2 = uint16(2) // vectors + HNSW adjacency
)

// TKVXMeta describes a decoded snapshot header.
type TKVXMeta struct {
	Version uint16
	Col     string
	Dim     int
	Metric  string
	Count   int
	HasGraph bool
}

// TKVXGraph holds optional HNSW adjacency for TKVX v2.
type TKVXGraph struct {
	Entry    string
	MaxLayer int
	Nodes    map[string][][]string // id → layers → neighbor ids
}

// WriteTKVXSnapshot writes vectors in T2.12 binary format (v1).
func WriteTKVXSnapshot(path, col string, dim int, metric string, vectors map[string][]float32) error {
	return WriteTKVXSnapshotEx(path, col, dim, metric, vectors, nil)
}

// WriteTKVXSnapshotEx writes v1 (vectors) or v2 (vectors+graph).
// Layout (little-endian):
//
//	magic(4) version(u16) col_len(u16) col dim(u32) metric_len(u16) metric
//	count(u32) crc32(u32) payload
//
// payload v1: [id_len(u16) id float32[dim]]×count
// payload v2: v1 vectors + entry_len(u16) entry + max_layer(u16)
//	+ for each vector key in same order: n_layers(u8) [n_nb(u16) nb_idx(u32)×n_nb]×n_layers
func WriteTKVXSnapshotEx(path, col string, dim int, metric string, vectors map[string][]float32, graph *TKVXGraph) error {
	if metric == "" {
		metric = "cosine"
	}
	keys := make([]string, 0, len(vectors))
	for k, v := range vectors {
		if len(v) != dim {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	payload := make([]byte, 0, len(keys)*(2+32+dim*4))
	for _, k := range keys {
		idb := []byte(k)
		if len(idb) > math.MaxUint16 {
			return errors.New("chunk_id too long for TKVX")
		}
		var lenbuf [2]byte
		binary.LittleEndian.PutUint16(lenbuf[:], uint16(len(idb)))
		payload = append(payload, lenbuf[:]...)
		payload = append(payload, idb...)
		for _, f := range vectors[k] {
			var fb [4]byte
			binary.LittleEndian.PutUint32(fb[:], math.Float32bits(f))
			payload = append(payload, fb[:]...)
		}
	}

	version := tkvxVersion1
	if graph != nil && len(graph.Nodes) > 0 {
		version = tkvxVersion2
		idxOf := make(map[string]uint32, len(keys))
		for i, k := range keys {
			idxOf[k] = uint32(i)
		}
		entry := []byte(graph.Entry)
		if len(entry) > math.MaxUint16 {
			return errors.New("entry id too long")
		}
		var u16 [2]byte
		binary.LittleEndian.PutUint16(u16[:], uint16(len(entry)))
		payload = append(payload, u16[:]...)
		payload = append(payload, entry...)
		binary.LittleEndian.PutUint16(u16[:], uint16(graph.MaxLayer))
		payload = append(payload, u16[:]...)

		for _, k := range keys {
			layers := graph.Nodes[k]
			nL := len(layers)
			if nL > 255 {
				nL = 255
			}
			payload = append(payload, byte(nL))
			for li := 0; li < nL; li++ {
				nbs := layers[li]
				var valid []uint32
				for _, nb := range nbs {
					if ix, ok := idxOf[nb]; ok {
						valid = append(valid, ix)
					}
				}
				binary.LittleEndian.PutUint16(u16[:], uint16(len(valid)))
				payload = append(payload, u16[:]...)
				var u32 [4]byte
				for _, ix := range valid {
					binary.LittleEndian.PutUint32(u32[:], ix)
					payload = append(payload, u32[:]...)
				}
			}
		}
	}

	crc := crc32.ChecksumIEEE(payload)
	colb := []byte(col)
	metb := []byte(metric)
	hdr := make([]byte, 0, 4+2+2+len(colb)+4+2+len(metb)+4+4)
	hdr = append(hdr, tkvxMagic...)
	var bu16 [2]byte
	var bu32 [4]byte
	binary.LittleEndian.PutUint16(bu16[:], version)
	hdr = append(hdr, bu16[:]...)
	binary.LittleEndian.PutUint16(bu16[:], uint16(len(colb)))
	hdr = append(hdr, bu16[:]...)
	hdr = append(hdr, colb...)
	binary.LittleEndian.PutUint32(bu32[:], uint32(dim))
	hdr = append(hdr, bu32[:]...)
	binary.LittleEndian.PutUint16(bu16[:], uint16(len(metb)))
	hdr = append(hdr, bu16[:]...)
	hdr = append(hdr, metb...)
	binary.LittleEndian.PutUint32(bu32[:], uint32(len(keys)))
	hdr = append(hdr, bu32[:]...)
	binary.LittleEndian.PutUint32(bu32[:], crc)
	hdr = append(hdr, bu32[:]...)

	return atomicWriteFile(path, append(hdr, payload...))
}

// ReadTKVXSnapshot decodes vectors (v1/v2). Graph is discarded.
func ReadTKVXSnapshot(path string) (map[string][]float32, TKVXMeta, error) {
	vecs, meta, _, err := ReadTKVXSnapshotEx(path)
	return vecs, meta, err
}

// ReadTKVXSnapshotEx returns vectors and optional graph (v2).
func ReadTKVXSnapshotEx(path string) (map[string][]float32, TKVXMeta, *TKVXGraph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, TKVXMeta{}, nil, err
	}
	return decodeTKVX(raw)
}

func decodeTKVX(raw []byte) (map[string][]float32, TKVXMeta, *TKVXGraph, error) {
	var meta TKVXMeta
	if len(raw) < 4+2+2+4+2+4+4 || string(raw[:4]) != tkvxMagic {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	off := 4
	meta.Version = binary.LittleEndian.Uint16(raw[off:])
	off += 2
	colLen := int(binary.LittleEndian.Uint16(raw[off:]))
	off += 2
	if off+colLen > len(raw) {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	meta.Col = string(raw[off : off+colLen])
	off += colLen
	if off+4 > len(raw) {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	meta.Dim = int(binary.LittleEndian.Uint32(raw[off:]))
	off += 4
	if off+2 > len(raw) {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	metLen := int(binary.LittleEndian.Uint16(raw[off:]))
	off += 2
	if off+metLen > len(raw) {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	meta.Metric = string(raw[off : off+metLen])
	off += metLen
	if off+8 > len(raw) {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	meta.Count = int(binary.LittleEndian.Uint32(raw[off:]))
	off += 4
	wantCRC := binary.LittleEndian.Uint32(raw[off:])
	off += 4
	payload := raw[off:]
	if crc32.ChecksumIEEE(payload) != wantCRC {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	out := make(map[string][]float32, meta.Count)
	keys := make([]string, 0, meta.Count)
	for i := 0; i < meta.Count; i++ {
		if len(payload) < 2 {
			return nil, meta, nil, ErrCorruptSnapshot
		}
		idLen := int(binary.LittleEndian.Uint16(payload))
		payload = payload[2:]
		if len(payload) < idLen+meta.Dim*4 {
			return nil, meta, nil, ErrCorruptSnapshot
		}
		id := string(payload[:idLen])
		payload = payload[idLen:]
		vec := make([]float32, meta.Dim)
		for d := 0; d < meta.Dim; d++ {
			vec[d] = math.Float32frombits(binary.LittleEndian.Uint32(payload))
			payload = payload[4:]
		}
		out[id] = vec
		keys = append(keys, id)
	}

	var graph *TKVXGraph
	if meta.Version >= tkvxVersion2 {
		meta.HasGraph = true
		if len(payload) < 2 {
			return nil, meta, nil, ErrCorruptSnapshot
		}
		eLen := int(binary.LittleEndian.Uint16(payload))
		payload = payload[2:]
		if len(payload) < eLen+2 {
			return nil, meta, nil, ErrCorruptSnapshot
		}
		entry := string(payload[:eLen])
		payload = payload[eLen:]
		maxLayer := int(binary.LittleEndian.Uint16(payload))
		payload = payload[2:]
		nodes := make(map[string][][]string, len(keys))
		for _, k := range keys {
			if len(payload) < 1 {
				return nil, meta, nil, ErrCorruptSnapshot
			}
			nL := int(payload[0])
			payload = payload[1:]
			layers := make([][]string, nL)
			for li := 0; li < nL; li++ {
				if len(payload) < 2 {
					return nil, meta, nil, ErrCorruptSnapshot
				}
				nNb := int(binary.LittleEndian.Uint16(payload))
				payload = payload[2:]
				if len(payload) < nNb*4 {
					return nil, meta, nil, ErrCorruptSnapshot
				}
				nbs := make([]string, 0, nNb)
				for j := 0; j < nNb; j++ {
					ix := binary.LittleEndian.Uint32(payload)
					payload = payload[4:]
					if int(ix) < len(keys) {
						nbs = append(nbs, keys[ix])
					}
				}
				layers[li] = nbs
			}
			nodes[k] = layers
		}
		graph = &TKVXGraph{Entry: entry, MaxLayer: maxLayer, Nodes: nodes}
	}
	if len(payload) != 0 {
		return nil, meta, nil, ErrCorruptSnapshot
	}
	return out, meta, graph, nil
}

// loadSnapshotVectors tries TKVX first, then legacy JSON.
func loadSnapshotVectors(path string) (map[string][]float32, int, error) {
	vecs, meta, _, err := loadSnapshotFull(path)
	if err != nil {
		return nil, 0, err
	}
	return vecs, meta.Dim, nil
}

func loadSnapshotFull(path string) (map[string][]float32, TKVXMeta, *TKVXGraph, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, TKVXMeta{}, nil, err
	}
	if len(raw) >= 4 && string(raw[:4]) == tkvxMagic {
		return decodeTKVX(raw)
	}
	var sf snapshotFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return nil, TKVXMeta{}, nil, err
	}
	if sf.Vectors == nil {
		sf.Vectors = map[string][]float32{}
	}
	return sf.Vectors, TKVXMeta{Version: 0, Dim: sf.Dim, Count: len(sf.Vectors)}, nil, nil
}
