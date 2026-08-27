package main

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/seeinp/seeinp/internal/protocol"
)

func TestProtocolIntegration(t *testing.T) {
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
		t.Fatalf("WriteMessage failed: %v", err)
	}

	// Read message
	readMsg, err := codec.ReadMessage(&buf)
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	// Verify
	if readMsg.Type != msg.Type {
		t.Errorf("Type = %v, want %v", readMsg.Type, msg.Type)
	}

	fmt.Println("Protocol integration test passed!")
}
