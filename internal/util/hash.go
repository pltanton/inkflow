package util

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

func SHA256Hex(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func HashAndCopy(dst io.Writer, src io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
