package integrity

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

func VerifyFile(path, sri string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	sha256Hash := sha256.New()
	var upstream hash.Hash
	var expected []byte
	if sri != "" {
		algorithm, encoded, ok := strings.Cut(sri, "-")
		if !ok || encoded == "" {
			return "", 0, fmt.Errorf("invalid integrity value %q", sri)
		}
		expected, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", 0, fmt.Errorf("decode integrity value: %w", err)
		}
		switch strings.ToLower(algorithm) {
		case "sha512":
			upstream = sha512.New()
		case "sha384":
			upstream = sha512.New384()
		case "sha256":
			upstream = sha256.New()
		case "sha1":
			upstream = sha1.New()
		default:
			return "", 0, fmt.Errorf("unsupported integrity algorithm %q", algorithm)
		}
	}

	writers := []io.Writer{sha256Hash}
	if upstream != nil {
		writers = append(writers, upstream)
	}
	n, err := io.Copy(io.MultiWriter(writers...), f)
	if err != nil {
		return "", n, err
	}
	if upstream != nil && subtle.ConstantTimeCompare(upstream.Sum(nil), expected) != 1 {
		return "", n, fmt.Errorf("upstream integrity mismatch")
	}
	return hex.EncodeToString(sha256Hash.Sum(nil)), n, nil
}
