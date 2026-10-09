package diskhealth

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

func digestReader(reader io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
