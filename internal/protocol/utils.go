package protocol

import (
	"crypto/rand"
	"fmt"
)

// GenerateID generates a unique message ID
func GenerateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
