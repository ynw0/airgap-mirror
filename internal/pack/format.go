package pack

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	PackHeaderSize   = 128
	RecordHeaderSize = 80
	Version          = uint16(1)
)

var packMagic = []byte("AGMPACK1")
var recordMagic = []byte("AGMREC01")

func uuidBytes(id string) ([16]byte, error) {
	var o [16]byte
	s := strings.ReplaceAll(id, "-", "")
	if len(s) != 32 {
		return o, fmt.Errorf("invalid uuid %q", id)
	}
	b, e := hex.DecodeString(s)
	if e != nil {
		return o, e
	}
	copy(o[:], b)
	return o, nil
}
func uuidString(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func put16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func put32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func put64(b []byte, v uint64) { binary.LittleEndian.PutUint64(b, v) }
