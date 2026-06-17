package store

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
	"testing"

	"github.com/transparency-dev/merkle/rfc6962"
)

func TestSerializeDeserializeRange(t *testing.T) {
	// Test cases with different sizes
	sizes := []uint64{0, 1, 2, 3, 4, 5, 7, 8, 15, 16, 17, 100, 255, 256, 1000}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			// Build a range of the given size
			r := fact.NewEmptyRange(0)
			for i := uint64(0); i < size; i++ {
				leafData := []byte(fmt.Sprintf("leaf-%d", i))
				leafHash := rfc6962.DefaultHasher.HashLeaf(leafData)
				err := r.Append(leafHash, nil)
				if err != nil {
					t.Fatalf("Failed to append leaf %d: %v", i, err)
				}
			}

			if r.End() != size {
				t.Fatalf("Expected range end to be %d, got %d", size, r.End())
			}

			// Serialize
			data := SerializeRange(r)

			// Verify serialization format
			expectedNumHashes := bits.OnesCount64(size)
			expectedLen := 8 + expectedNumHashes*32
			if len(data) != expectedLen {
				t.Errorf("Serialized data length mismatch: expected %d, got %d", expectedLen, len(data))
			}

			// Deserialize
			r2, err := DeserializeRange(data)
			if err != nil {
				t.Fatalf("Failed to deserialize: %v", err)
			}

			// Assert identical
			if r2.Begin() != r.Begin() {
				t.Errorf("Begin mismatch: expected %d, got %d", r.Begin(), r2.Begin())
			}
			if r2.End() != r.End() {
				t.Errorf("End mismatch: expected %d, got %d", r.End(), r2.End())
			}

			hashes := r.Hashes()
			hashes2 := r2.Hashes()
			if len(hashes) != len(hashes2) {
				t.Fatalf("Hashes length mismatch: expected %d, got %d", len(hashes), len(hashes2))
			}
			for i := range hashes {
				if !bytes.Equal(hashes[i], hashes2[i]) {
					t.Errorf("Hash %d mismatch", i)
				}
			}

			root, err := r.GetRootHash(nil)
			if err != nil {
				t.Fatalf("Failed to get root hash for r: %v", err)
			}
			root2, err := r2.GetRootHash(nil)
			if err != nil {
				t.Fatalf("Failed to get root hash for r2: %v", err)
			}
			if !bytes.Equal(root, root2) {
				t.Errorf("Root hash mismatch: expected %x, got %x", root, root2)
			}
		})
	}
}

func TestDeserializeCorruptData(t *testing.T) {
	// Too short
	if _, err := DeserializeRange([]byte{1, 2, 3}); err == nil {
		t.Error("Expected error for too short data")
	}

	// Size mismatch with data length
	// Size 1 needs 8 + 32 = 40 bytes. Provide 8 + 31 = 39 bytes.
	data := make([]byte, 39)
	binary.BigEndian.PutUint64(data[0:8], 1)
	if _, err := DeserializeRange(data); err == nil {
		t.Error("Expected error for size mismatch (too short for hashes)")
	}

	// Provide 8 + 33 = 41 bytes for size 1.
	data = make([]byte, 41)
	binary.BigEndian.PutUint64(data[0:8], 1)
	if _, err := DeserializeRange(data); err == nil {
		t.Error("Expected error for size mismatch (too long for hashes)")
	}
}
