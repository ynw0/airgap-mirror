package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func FileSHA256(p string) (string, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return "", n, e
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func decodeSHA256(s string) ([32]byte, error) {
	var o [32]byte
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != 32 {
		return o, fmt.Errorf("invalid sha256 %q", s)
	}
	copy(o[:], b)
	return o, nil
}
