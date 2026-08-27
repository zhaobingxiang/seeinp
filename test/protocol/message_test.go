package protocol

import (
	"bytes"
	"testing"

	"github.com/seeinp/seeinp/internal/protocol"
)

func TestMessageCodec(t *testing.T) {
	// Create a message
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

	// Create codec
	codec := protocol.NewCodec()

	// Create buffer
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

	if readMsg.ID != msg.ID {
		t.Errorf("ID = %v, want %v", readMsg.ID, msg.ID)
	}

	if readMsg.Ts != msg.Ts {
		t.Errorf("Ts = %v, want %v", readMsg.Ts, msg.Ts)
	}
}

func TestGenerateID(t *testing.T) {
	id1 := protocol.GenerateID()
	id2 := protocol.GenerateID()

	if id1 == id2 {
		t.Error("GenerateID should generate unique IDs")
	}

	if len(id1) != 32 { // 16 bytes = 32 hex chars
		t.Errorf("ID length = %v, want 32", len(id1))
	}
}
