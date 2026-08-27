package main

import (
	"bytes"
	"fmt"

	"github.com/seeinp/seeinp/internal/protocol"
)

func main() {
	// Test message encoding/decoding
	msg := &protocol.Message{
		Type: protocol.TypeHello,
		ID:   protocol.GenerateID(),
		Ts:   1234567890,
		Data: &protocol.HelloData{
			Version: "1.0.0",
			Arch:    "amd64",
			OS:      "linux",
		},
	}

	codec := protocol.NewCodec()
	var buf bytes.Buffer

	// Write message
	if err := codec.WriteMessage(&buf, msg); err != nil {
		fmt.Printf("WriteMessage failed: %v\n", err)
		return
	}

	// Read message
	readMsg, err := codec.ReadMessage(&buf)
	if err != nil {
		fmt.Printf("ReadMessage failed: %v\n", err)
		return
	}

	// Verify
	if readMsg.Type != msg.Type {
		fmt.Printf("Type = %v, want %v\n", readMsg.Type, msg.Type)
		return
	}

	fmt.Println("✓ Protocol test passed!")
	fmt.Printf("  Message type: %s\n", readMsg.Type)
	fmt.Printf("  Message ID: %s\n", readMsg.ID)
	fmt.Printf("  Timestamp: %d\n", readMsg.Ts)
}
