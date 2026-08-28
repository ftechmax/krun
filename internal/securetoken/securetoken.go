package securetoken

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// Hex returns a cryptographically random token containing byteLength bytes.
func Hex(byteLength int) (string, error) {
	return hexFrom(rand.Reader, byteLength)
}

func hexFrom(reader io.Reader, byteLength int) (string, error) {
	if byteLength <= 0 {
		return "", fmt.Errorf("token byte length must be positive")
	}

	buffer := make([]byte, byteLength)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", fmt.Errorf("read secure random bytes: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
