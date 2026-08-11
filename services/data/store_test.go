package data

import (
	"encoding/binary"
	"testing"
)

func TestEncodeRequestLayout(t *testing.T) {
	req := encodeRequest(cmdPut, []byte("k"), []byte("v"))
	if len(req) != 11+1+1 {
		t.Fatalf("len=%d", len(req))
	}
	if binary.LittleEndian.Uint16(req[0:2]) != mkMagic {
		t.Fatal("magic")
	}
	if req[2] != cmdPut {
		t.Fatal("cmd")
	}
	if binary.LittleEndian.Uint32(req[3:7]) != 1 {
		t.Fatal("key_len")
	}
	if binary.LittleEndian.Uint32(req[7:11]) != 1 {
		t.Fatal("val_len")
	}
}

func TestDecodeScanPayload(t *testing.T) {
	payload := make([]byte, 0, 64)
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, 2)
	payload = append(payload, buf...)
	for _, kv := range []KVPair{{"a", "1"}, {"b", "2"}} {
		binary.LittleEndian.PutUint32(buf, uint32(len(kv.Key)))
		payload = append(payload, buf...)
		payload = append(payload, kv.Key...)
		binary.LittleEndian.PutUint32(buf, uint32(len(kv.Value)))
		payload = append(payload, buf...)
		payload = append(payload, kv.Value...)
	}
	items, err := decodeScanPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Key != "a" || items[1].Value != "2" {
		t.Fatalf("%+v", items)
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	s := NewStore()
	if err := s.Put("x", "y"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get("x")
	if err != nil || !ok || v != "y" {
		t.Fatalf("get %v %v %v", v, ok, err)
	}
	items, err := s.Scan("a", "z")
	if err != nil || len(items) != 1 {
		t.Fatalf("scan %+v %v", items, err)
	}
}
