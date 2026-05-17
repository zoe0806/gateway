package tools

import (
	"crypto/sha256"
	"encoding/hex"
)

func HashSession(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}
