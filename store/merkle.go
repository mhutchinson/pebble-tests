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

// serializeUint16Slice serializes a slice of uint16.
func serializeUint16Slice(slice []uint16) []byte {
	buf := make([]byte, 2*len(slice))
	for i, v := range slice {
		binary.BigEndian.PutUint16(buf[2*i:2*i+2], v)
	}
	return buf
}

// deserializeUint16Slice deserializes a slice of uint16.
func deserializeUint16Slice(buf []byte) ([]uint16, error) {
	if len(buf)%2 != 0 {
		return nil, fmt.Errorf("invalid buffer length for uint16 slice: %d", len(buf))
	}
	slice := make([]uint16, len(buf)/2)
	for i := 0; i < len(slice); i++ {
		slice[i] = binary.BigEndian.Uint16(buf[2*i : 2*i+2])
	}
	return slice, nil
}

// serializeChunkValue serializes the compact.Range and relativeIndices into a single byte slice.
func serializeChunkValue(r *compact.Range, relativeIndices []uint16) []byte {
	serializedRange := SerializeRange(r)
	relBytes := serializeUint16Slice(relativeIndices)

	buf := make([]byte, len(serializedRange)+len(relBytes))
	copy(buf[0:len(serializedRange)], serializedRange)
	copy(buf[len(serializedRange):], relBytes)
	return buf
}

// deserializeChunkValue parses the compact.Range and relativeIndices from a byte slice.
func deserializeChunkValue(data []byte) (*compact.Range, []uint16, error) {
	if len(data) < 8 {
		return nil, nil, fmt.Errorf("deserializeChunkValue: data too short: %d bytes", len(data))
	}
	size := binary.BigEndian.Uint64(data[0:8])
	numHashes := bits.OnesCount64(size)
	expectedRangeLen := 8 + numHashes*32
	if len(data) < expectedRangeLen {
		return nil, nil, fmt.Errorf("deserializeChunkValue: data too short for compact range: expected >= %d, got %d", expectedRangeLen, len(data))
	}

	rangeBytes := data[0:expectedRangeLen]
	r, err := DeserializeRange(rangeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("deserializeChunkValue: failed to deserialize range: %w", err)
	}

	relBytes := data[expectedRangeLen:]
	relIndices, err := deserializeUint16Slice(relBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("deserializeChunkValue: failed to deserialize relative indices: %w", err)
	}

	return r, relIndices, nil
}

