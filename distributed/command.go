package distributed

import "encoding/json"

const (
	opPut    = "put"
	opDelete = "del"
)

// command is the Raft log payload applied by KVFSM.
type command struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
}

func encodeCommand(c *command) ([]byte, error) {
	return json.Marshal(c)
}

func decodeCommand(b []byte) (*command, error) {
	var c command
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
