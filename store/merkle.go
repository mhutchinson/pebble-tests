package store

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/transparency-dev/merkle/compact"
	"github.com/transparency-dev/merkle/rfc6962"
)

var fact = compact.RangeFactory{
	Hash: rfc6962.DefaultHasher.HashChildren,
}

// SerializeRange serializes a compact.Range.
// It assumes the range begins at 0.
func SerializeRange(r *compact.Range) []byte {
	if r.Begin() != 0 {
		panic(fmt.Sprintf("SerializeRange: range begin must be 0, got %d", r.Begin()))
	}
	size := r.End()
	hashes := r.Hashes()
	numHashes := bits.OnesCount64(size)
	if len(hashes) != numHashes {
		panic(fmt.Sprintf("SerializeRange: expected %d hashes, got %d", numHashes, len(hashes)))
	}

	buf := make([]byte, 8+numHashes*32)
	binary.BigEndian.PutUint64(buf[0:8], size)
	for i, h := range hashes {
		if len(h) != 32 {
			panic(fmt.Sprintf("SerializeRange: hash %d has invalid length %d", i, len(h)))
		}
		copy(buf[8+i*32:8+(i+1)*32], h)
	}
	return buf
}

// DeserializeRange deserializes a compact.Range.
func DeserializeRange(data []byte) (*compact.Range, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("DeserializeRange: data too short: %d bytes", len(data))
	}
	size := binary.BigEndian.Uint64(data[0:8])
	numHashes := bits.OnesCount64(size)
	expectedLen := 8 + numHashes*32
	if len(data) != expectedLen {
		return nil, fmt.Errorf("DeserializeRange: data length mismatch: expected %d, got %d", expectedLen, len(data))
	}

	hashes := make([][]byte, numHashes)
	for i := 0; i < numHashes; i++ {
		hashes[i] = make([]byte, 32)
		copy(hashes[i], data[8+i*32:8+(i+1)*32])
	}

	return fact.NewRange(0, size, hashes)
}
