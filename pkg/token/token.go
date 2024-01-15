package token

import (
	"crypto/rand"
	"encoding/hex"
)

type Generator struct {
	tokenLen int
}

func NewGenerator(length int) Generator {
	return Generator{
		tokenLen: length,
	}
}

func (g Generator) GenerateToken() (string, error) {
	b := make([]byte, g.tokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
