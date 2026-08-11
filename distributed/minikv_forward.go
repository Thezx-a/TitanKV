package distributed

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Minimal MiniKV TCP client (same wire format as services/data MiniKVClient).
// Kept local so distributed/ does not import the data service package.

const (
	mkMagic     uint16 = 0x4D4B
	cmdPut      uint8  = 1
	cmdDel      uint8  = 3
	statusOK    uint8  = 0
)

// WriteForwarder optionally mirrors FSM writes to an external store (MiniKV).
type WriteForwarder interface {
	Put(key, value string) error
	Delete(key string) error
	Close() error
}

type miniKVForwarder struct {
	addr    string
	timeout time.Duration
	mu      sync.Mutex
	conn    net.Conn
}

func newMiniKVForwarder(addr string) *miniKVForwarder {
	return &miniKVForwarder{addr: addr, timeout: 3 * time.Second}
}

func (c *miniKVForwarder) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *miniKVForwarder) Put(key, value string) error {
	return c.roundTrip(cmdPut, []byte(key), []byte(value))
}

func (c *miniKVForwarder) Delete(key string) error {
	return c.roundTrip(cmdDel, []byte(key), nil)
}

func (c *miniKVForwarder) dial() (net.Conn, error) {
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

func (c *miniKVForwarder) reset() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *miniKVForwarder) roundTrip(cmd uint8, key, value []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := c.dial()
		if err != nil {
			return err
		}
		_ = conn.SetDeadline(time.Now().Add(c.timeout))

		req := make([]byte, 11+len(key)+len(value))
		binary.LittleEndian.PutUint16(req[0:2], mkMagic)
		req[2] = cmd
		binary.LittleEndian.PutUint32(req[3:7], uint32(len(key)))
		binary.LittleEndian.PutUint32(req[7:11], uint32(len(value)))
		copy(req[11:], key)
		copy(req[11+len(key):], value)

		if _, err := conn.Write(req); err != nil {
			lastErr = err
			c.reset()
			continue
		}
		st, msg, err := readMiniKVResponse(conn)
		if err != nil {
			lastErr = err
			c.reset()
			continue
		}
		if st != statusOK {
			return fmt.Errorf("minikv status=%d %s", st, string(msg))
		}
		return nil
	}
	return lastErr
}

func readMiniKVResponse(r io.Reader) (uint8, []byte, error) {
	hdr := make([]byte, 7)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	if binary.LittleEndian.Uint16(hdr[0:2]) != mkMagic {
		return 0, nil, fmt.Errorf("bad minikv magic")
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
