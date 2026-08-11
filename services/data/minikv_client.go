package data

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	mkMagic     uint16 = 0x4D4B
	cmdPut      uint8  = 1
	cmdGet      uint8  = 2
	cmdDel      uint8  = 3
	cmdScan     uint8  = 4
	statusOK    uint8  = 0
	statusMiss  uint8  = 1
	statusError uint8  = 2
)

// MiniKVClient talks to C++ minikv_server over the native binary protocol
// (see minikv/src/network/protocol.h). Little-endian, packed headers.
type MiniKVClient struct {
	addr    string
	timeout time.Duration
	mu      sync.Mutex
	conn    net.Conn
}

func NewMiniKVClient(addr string) *MiniKVClient {
	return &MiniKVClient{addr: addr, timeout: 3 * time.Second}
}

func (c *MiniKVClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *MiniKVClient) dial() (net.Conn, error) {
	if c.conn != nil {
		return c.conn, nil
	}
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, fmt.Errorf("minikv dial %s: %w", c.addr, err)
	}
	c.conn = conn
	return conn, nil
}

func (c *MiniKVClient) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *MiniKVClient) roundTrip(cmd uint8, key, value []byte) (uint8, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := c.dial()
		if err != nil {
			return 0, nil, err
		}
		_ = conn.SetDeadline(time.Now().Add(c.timeout))

		req := encodeRequest(cmd, key, value)
		if _, err := conn.Write(req); err != nil {
			lastErr = err
			c.reset()
			continue
		}
		st, payload, err := readResponse(conn)
		if err != nil {
			lastErr = err
			c.reset()
			continue
		}
		return st, payload, nil
	}
	return 0, nil, lastErr
}

func encodeRequest(cmd uint8, key, value []byte) []byte {
	// magic(2) + cmd(1) + key_len(4) + val_len(4) + key + value = 11 + ...
	buf := make([]byte, 11+len(key)+len(value))
	binary.LittleEndian.PutUint16(buf[0:2], mkMagic)
	buf[2] = cmd
	binary.LittleEndian.PutUint32(buf[3:7], uint32(len(key)))
	binary.LittleEndian.PutUint32(buf[7:11], uint32(len(value)))
	copy(buf[11:], key)
	copy(buf[11+len(key):], value)
	return buf
}

func readResponse(r io.Reader) (uint8, []byte, error) {
	hdr := make([]byte, 7) // magic(2)+status(1)+val_len(4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	magic := binary.LittleEndian.Uint16(hdr[0:2])
	if magic != mkMagic {
		return 0, nil, fmt.Errorf("bad response magic %#x", magic)
	}
	status := hdr[2]
	valLen := binary.LittleEndian.Uint32(hdr[3:7])
	if valLen == 0 {
		return status, nil, nil
	}
	payload := make([]byte, valLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return status, payload, nil
}

func (c *MiniKVClient) Put(key, value string) error {
	st, msg, err := c.roundTrip(cmdPut, []byte(key), []byte(value))
	if err != nil {
		return err
	}
	if st != statusOK {
		return fmt.Errorf("minikv put: status=%d %s", st, string(msg))
	}
	return nil
}

func (c *MiniKVClient) Get(key string) (string, bool, error) {
	st, payload, err := c.roundTrip(cmdGet, []byte(key), nil)
	if err != nil {
		return "", false, err
	}
	switch st {
	case statusOK:
		return string(payload), true, nil
	case statusMiss:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("minikv get: status=%d %s", st, string(payload))
	}
}

func (c *MiniKVClient) Delete(key string) error {
	st, msg, err := c.roundTrip(cmdDel, []byte(key), nil)
	if err != nil {
		return err
	}
	if st != statusOK {
		return fmt.Errorf("minikv del: status=%d %s", st, string(msg))
	}
	return nil
}

func (c *MiniKVClient) Scan(start, end string) ([]KVPair, error) {
	st, payload, err := c.roundTrip(cmdScan, []byte(start), []byte(end))
	if err != nil {
		return nil, err
	}
	if st != statusOK {
		return nil, fmt.Errorf("minikv scan: status=%d %s", st, string(payload))
	}
	return decodeScanPayload(payload)
}

func decodeScanPayload(payload []byte) ([]KVPair, error) {
	if len(payload) < 4 {
		return nil, errors.New("scan payload too short")
	}
	n := binary.LittleEndian.Uint32(payload[:4])
	off := 4
	out := make([]KVPair, 0, n)
	for i := uint32(0); i < n; i++ {
		if off+4 > len(payload) {
			return nil, errors.New("scan truncated key_len")
		}
		klen := int(binary.LittleEndian.Uint32(payload[off : off+4]))
		off += 4
		if off+klen+4 > len(payload) {
			return nil, errors.New("scan truncated key")
		}
		key := string(payload[off : off+klen])
		off += klen
		vlen := int(binary.LittleEndian.Uint32(payload[off : off+4]))
		off += 4
		if off+vlen > len(payload) {
			return nil, errors.New("scan truncated value")
		}
		val := string(payload[off : off+vlen])
		off += vlen
		out = append(out, KVPair{Key: key, Value: val})
	}
	return out, nil
}
