package harness

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync/atomic"
)

// Generator generates workloads for benchmarking.
type Generator struct {
	mode    string
	numKeys int
	rng     *rand.Rand
	zipf    *rand.Zipf
}

// NewGenerator creates a new workload generator.
// Mode A: Uniform random key selection.
// Mode B: Sequential key selection (round-robin).
// Mode C: Zipfian key selection.
func NewGenerator(mode string, numKeys int) (*Generator, error) {
	if mode != "A" && mode != "B" && mode != "C" {
		return nil, fmt.Errorf("invalid mode: %s, must be A, B, or C", mode)
	}
	if numKeys <= 0 {
		return nil, fmt.Errorf("numKeys must be > 0")
	}

	r := rand.New(rand.NewSource(42))
	var z *rand.Zipf
	if mode == "C" {
		// imax = numKeys - 1
		z = rand.NewZipf(r, 1.1, 1.0, uint64(numKeys-1))
		if z == nil {
			return nil, fmt.Errorf("failed to create Zipf generator")
		}
	}

	return &Generator{
		mode:    mode,
		numKeys: numKeys,
		rng:     r,
		zipf:    z,
	}, nil
}

// NextBatch generates a batch of updates.
// It generates `size` sequential indices starting from `startIdx`.
// These indices are mapped to keys based on the generator mode,
// hashed with SHA-256, grouped by key, and returned.
func (g *Generator) NextBatch(size int, startIdx uint64) map[[32]byte][]uint64 {
	batch := make(map[[32]byte][]uint64)
	for i := 0; i < size; i++ {
		idx := startIdx + uint64(i)
		var keyID uint64

		switch g.mode {
		case "A": // Uniform
			keyID = uint64(g.rng.Intn(g.numKeys))
		case "B": // Sequential
			keyID = idx % uint64(g.numKeys)
		case "C": // Zipfian
			keyID = g.zipf.Uint64()
		}

		keyBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(keyBytes, keyID)
		hash := sha256.Sum256(keyBytes)

		batch[hash] = append(batch[hash], idx)
	}
	return batch
}

// ReadGenerator generates read queries.
type ReadGenerator struct {
	mode    string
	numKeys int
	maxIdx  *uint64
	rng     *rand.Rand
	zipf    *rand.Zipf
}

// NewReadGenerator creates a new read workload generator.
func NewReadGenerator(mode string, numKeys int, maxIdx *uint64) (*ReadGenerator, error) {
	if mode != "A" && mode != "B" && mode != "C" {
		return nil, fmt.Errorf("invalid mode: %s, must be A, B, or C", mode)
	}
	if numKeys <= 0 {
		return nil, fmt.Errorf("numKeys must be > 0")
	}
	if maxIdx == nil {
		return nil, fmt.Errorf("maxIdx must not be nil")
	}

	r := rand.New(rand.NewSource(42))
	var z *rand.Zipf
	if mode == "C" {
		z = rand.NewZipf(r, 1.1, 1.0, uint64(numKeys-1))
		if z == nil {
			return nil, fmt.Errorf("failed to create Zipf generator")
		}
	}

	return &ReadGenerator{
		mode:    mode,
		numKeys: numKeys,
		maxIdx:  maxIdx,
		rng:     r,
		zipf:    z,
	}, nil
}

// NextQuery generates a read query.
// It returns a key hash and a start offset.
func (g *ReadGenerator) NextQuery() ([32]byte, uint64) {
	var keyID uint64

	switch g.mode {
	case "A": // Random key index in [0, 9]
		keyID = uint64(g.rng.Intn(10))
	case "B": // Pick random written index I in [0, limit-1], key index is I % numKeys
		limit := atomic.LoadUint64(g.maxIdx)
		if limit == 0 {
			keyID = 0
		} else {
			I := g.rng.Uint64() % limit
			keyID = I % uint64(g.numKeys)
		}
	case "C": // Zipfian key index in [0, numKeys-1]
		keyID = g.zipf.Uint64()
	}

	keyBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyBytes, keyID)
	hash := sha256.Sum256(keyBytes)

	start := uint64(0)
	if g.rng.Float64() < 0.2 {
		start = uint64(g.rng.Intn(6)) // 0 to 5
	}

	return hash, start
}
