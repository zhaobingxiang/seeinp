package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Codec handles encoding and decoding of protocol messages
type Codec struct{}

// NewCodec creates a new codec
func NewCodec() *Codec {
	return &Codec{}
}

// WriteMessage writes a message to the writer with length prefix
func (c *Codec) WriteMessage(w io.Writer, msg *Message) error {
	// Marshal message to JSON
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// Write 4-byte big-endian length prefix
	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}

	// Write JSON payload
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}

	return nil
}

// ReadMessage reads a message from the reader
func (c *Codec) ReadMessage(r io.Reader) (*Message, error) {
	// Read 4-byte big-endian length prefix
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}

	// Sanity check
	if length > 10*1024*1024 { // 10MB max
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// Read JSON payload
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	// Unmarshal message
	msg := &Message{}
	if err := json.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return msg, nil
}
