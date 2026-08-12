package harness

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func TestNewGenerator(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		numKeys int
		wantErr bool
	}{
		{"valid A", "A", 100, false},
		{"valid B", "B", 100, false},
		{"valid C", "C", 100, false},
		{"invalid mode", "D", 100, true},
		{"invalid numKeys", "A", 0, true},
		{"negative numKeys", "A", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewGenerator(tt.mode, tt.numKeys)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewGenerator() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGenerator_ModeB_Sequential(t *testing.T) {
	numKeys := 5
	g, err := NewGenerator("B", numKeys)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	// Batch size 10, startIdx 0
	// Expected key IDs: 0, 1, 2, 3, 4, 0, 1, 2, 3, 4
	// Indices: 0..9
	batch := g.NextBatch(10, 0)

	expected := map[uint64][]uint64{
		0: {0, 5},
		1: {1, 6},
		2: {2, 7},
		3: {3, 8},
		4: {4, 9},
	}

	for keyID, expectedIndices := range expected {
		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, keyID)
		hash := sha256.Sum256(keyBytes)

		indices, ok := batch[hash]
		if !ok {
			t.Errorf("expected key %d (hash %x) not found in batch", keyID, hash)
			continue
		}

		if len(indices) != len(expectedIndices) {
			t.Errorf("key %d: expected %d indices, got %d", keyID, len(expectedIndices), len(indices))
			continue
		}

		for i, idx := range indices {
			if idx != expectedIndices[i] {
				t.Errorf("key %d: expected index at %d to be %d, got %d", keyID, i, expectedIndices[i], idx)
			}
		}
	}
}

func TestGenerator_Determinism(t *testing.T) {
	modes := []string{"A", "C"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			g1, _ := NewGenerator(mode, 100)
			g2, _ := NewGenerator(mode, 100)

			batch1 := g1.NextBatch(100, 0)
			batch2 := g2.NextBatch(100, 0)

			if len(batch1) != len(batch2) {
				t.Errorf("batches have different sizes: %d vs %d", len(batch1), len(batch2))
			}

			for k, v1 := range batch1 {
				v2, ok := batch2[k]
				if !ok {
					t.Errorf("key %x missing from batch2", k)
					continue
				}
				if len(v1) != len(v2) {
					t.Errorf("key %x has different number of indices: %d vs %d", k, len(v1), len(v2))
					continue
				}
				for i := range v1 {
					if v1[i] != v2[i] {
						t.Errorf("key %x index mismatch at %d: %d vs %d", k, i, v1[i], v2[i])
					}
				}
			}
		})
	}
}

func TestNewReadGenerator(t *testing.T) {
	zero := uint64(0)
	tests := []struct {
		name    string
		mode    string
		numKeys int
		maxIdx  *uint64
		wantErr bool
	}{
		{"valid A", "A", 100, &zero, false},
		{"valid B", "B", 100, &zero, false},
		{"valid C", "C", 100, &zero, false},
		{"invalid mode", "D", 100, &zero, true},
		{"invalid numKeys", "A", 0, &zero, true},
		{"nil maxIdx", "A", 100, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewReadGenerator(tt.mode, tt.numKeys, tt.maxIdx)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewReadGenerator() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestReadGenerator_ModeA(t *testing.T) {
	maxIdx := uint64(100)
	g, err := NewReadGenerator("A", 100, &maxIdx)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	// Mode A should generate keys in [0, 9]
	validHashes := make(map[[32]byte]bool)
	for i := uint64(0); i < 10; i++ {
		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, i)
		validHashes[sha256.Sum256(keyBytes)] = true
	}

	for i := 0; i < 1000; i++ {
		hash, _ := g.NextQuery()
		if !validHashes[hash] {
			t.Errorf("generated hash not in range [0, 9]")
		}
	}
}

func TestReadGenerator_ModeB(t *testing.T) {
	numKeys := 5
	maxIdx := uint64(0)
	g, err := NewReadGenerator("B", numKeys, &maxIdx)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	// If maxIdx is 0, it should return key ID 0
	keyBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyBytes, 0)
	expectedHash0 := sha256.Sum256(keyBytes)

	hash, _ := g.NextQuery()
	if hash != expectedHash0 {
		t.Errorf("expected key ID 0 when maxIdx is 0")
	}

	// Now set maxIdx to 10
	maxIdx = 10
	validHashes := make(map[[32]byte]bool)
	for i := uint64(0); i < 5; i++ {
		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, i)
		validHashes[sha256.Sum256(keyBytes)] = true
	}

	for i := 0; i < 1000; i++ {
		hash, _ := g.NextQuery()
		if !validHashes[hash] {
			t.Errorf("generated hash not in range [0, 4] when maxIdx is 10")
		}
	}
}

func TestReadGenerator_ModeC(t *testing.T) {
	numKeys := 10
	maxIdx := uint64(100)
	g, err := NewReadGenerator("C", numKeys, &maxIdx)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	validHashes := make(map[[32]byte]bool)
	for i := uint64(0); i < 10; i++ {
		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, i)
		validHashes[sha256.Sum256(keyBytes)] = true
	}

	for i := 0; i < 1000; i++ {
		hash, _ := g.NextQuery()
		if !validHashes[hash] {
			t.Errorf("generated hash not in range [0, 9] for Mode C")
		}
	}
}

func TestReadGenerator_StartOffset(t *testing.T) {
	maxIdx := uint64(100)
	g, err := NewReadGenerator("A", 100, &maxIdx)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	zeroCount := 0
	nonZeroCount := 0
	total := 10000

	for i := 0; i < total; i++ {
		_, start := g.NextQuery()
		if start == 0 {
			zeroCount++
		} else {
			nonZeroCount++
			if start > 5 {
				t.Errorf("start offset %d exceeds max 5", start)
			}
		}
	}

	expectedNonZero := float64(total) * 0.2 * (5.0 / 6.0)
	tolerance := float64(total) * 0.05

	if float64(nonZeroCount) < expectedNonZero-tolerance || float64(nonZeroCount) > expectedNonZero+tolerance {
		t.Errorf("unexpected non-zero start offset count: got %d, want around %f", nonZeroCount, expectedNonZero)
	}
}

func TestReadGenerator_Determinism(t *testing.T) {
	modes := []string{"A", "B", "C"}
	maxIdx := uint64(100)
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			g1, _ := NewReadGenerator(mode, 100, &maxIdx)
			g2, _ := NewReadGenerator(mode, 100, &maxIdx)

			for i := 0; i < 100; i++ {
				h1, s1 := g1.NextQuery()
				h2, s2 := g2.NextQuery()
				if h1 != h2 || s1 != s2 {
					t.Errorf("generators diverged at step %d", i)
				}
			}
		})
	}
}
