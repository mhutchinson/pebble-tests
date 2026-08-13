package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"github.com/cockroachdb/pebble"
	"github.com/transparency-dev/merkle/compact"
	"github.com/transparency-dev/merkle/rfc6962"
)

// ChunkScanStore implements the IndexStore interface using a chunked log construction
// in Pebble, optimized for range scans using the No-Seal layout.
type ChunkScanStore struct {
	db        *pebble.DB
	chunkSize uint64
}

// NewChunkScanStore opens a Pebble DB at the specified directory with the given chunk size.
func NewChunkScanStore(dir string, chunkSize uint64) (*ChunkScanStore, error) {
	if chunkSize == 0 || chunkSize > 65536 {
		return nil, fmt.Errorf("invalid chunkSize %d: must be > 0 and <= 65536", chunkSize)
	}
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %w", dir, err)
	}
	return &ChunkScanStore{
		db:        db,
		chunkSize: chunkSize,
	}, nil
}

// Compact runs a database compaction.
func (s *ChunkScanStore) Compact() error {
	return s.db.Compact([]byte{0}, []byte{0xff}, false)
}

// Close flushes all writes and closes the database.
func (s *ChunkScanStore) Close() error {
	return s.db.Close()
}

// WriteBatch writes a batch of updates atomically.
func (s *ChunkScanStore) WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error {
	// Share identical implementation with ChunkStore WriteBatch
	if len(updates) == 0 {
		return nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	iter, err := s.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	keys := sortedKeys(updates)

	for _, key := range keys {
		newIndices := updates[key]
		if len(newIndices) == 0 {
			continue
		}

		// Validate that newIndices are sorted
		for i := 1; i < len(newIndices); i++ {
			if newIndices[i] < newIndices[i-1] {
				return fmt.Errorf("indices are not sorted: key %x, index at %d (%d) < previous (%d)", key, i, newIndices[i], newIndices[i-1])
			}
		}

		prefix := make([]byte, 33)
		prefix[0] = chunkPrefix
		copy(prefix[1:], key[:])

		// Find previous latest chunk
		upperBound := prefixUpperBound(prefix)

		var prevKey []byte
		var prevVal []byte

		if iter.SeekLT(upperBound) {
			k := iter.Key()
			if bytes.HasPrefix(k, prefix) {
				prevKey = make([]byte, len(k))
				copy(prevKey, k)
				prevVal = make([]byte, len(iter.Value()))
				copy(prevVal, iter.Value())
			}
		}

		var currChunkNum uint64
		var currRange *compact.Range
		var currRelativeIndices []uint16
		var modified bool

		hasPrev := prevKey != nil
		if hasPrev {
			if len(prevKey) != 41 {
				return fmt.Errorf("invalid prevKey length: %d", len(prevKey))
			}
			prevChunkNum := binary.BigEndian.Uint64(prevKey[33:])

			pRange, pRelIndices, err := deserializeChunkValue(prevVal)
			if err != nil {
				return fmt.Errorf("failed to deserialize latest chunk value for key %x, chunk %d: %w", key, prevChunkNum, err)
			}

			currChunkNum = prevChunkNum
			currRange = pRange
			currRelativeIndices = pRelIndices
			modified = false
		} else {
			currChunkNum = newIndices[0] / s.chunkSize
			currRange = fact.NewEmptyRange(0)
			currRelativeIndices = []uint16{}
			modified = true
		}

		for _, idx := range newIndices {
			chunkNum := idx / s.chunkSize
			if chunkNum != currChunkNum {
				// Seal current chunk by writing its final state if it was modified.
				if modified {
					olderValBytes := serializeChunkValue(currRange, currRelativeIndices)
					dbKey := make([]byte, 41)
					copy(dbKey, prefix)
					binary.BigEndian.PutUint64(dbKey[33:], currChunkNum)
					if err := batch.Set(dbKey, olderValBytes, pebble.NoSync); err != nil {
						return fmt.Errorf("failed to write sealed chunk %d: %w", currChunkNum, err)
					}
				}

				// Compute finalized range in memory for the new chunk's starting_range.
				for _, rel := range currRelativeIndices {
					abs := currChunkNum*s.chunkSize + uint64(rel)
					var idxBytes [8]byte
					binary.BigEndian.PutUint64(idxBytes[:], abs)
					leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
					if err := currRange.Append(leafHash, nil); err != nil {
						return fmt.Errorf("failed to append leaf hash for index %d on seal: %w", abs, err)
					}
				}

				// Set up new chunk
				currChunkNum = chunkNum
				currRelativeIndices = []uint16{}
				modified = true
			}

			relIdx := uint16(idx % s.chunkSize)
			currRelativeIndices = append(currRelativeIndices, relIdx)
			modified = true
		}

		// Write final active chunk if modified
		if modified {
			latestValBytes := serializeChunkValue(currRange, currRelativeIndices)
			dbKey := make([]byte, 41)
			copy(dbKey, prefix)
			binary.BigEndian.PutUint64(dbKey[33:], currChunkNum)
			if err := batch.Set(dbKey, latestValBytes, pebble.NoSync); err != nil {
				return fmt.Errorf("failed to write latest chunk %d: %w", currChunkNum, err)
			}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}
	return nil
}

// Lookup returns all Input Log indices associated with the key,
// starting from the start-th match onwards (0-indexed).
func (s *ChunkScanStore) Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error) {
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	// Calculate a safe lower bound start chunk number
	startChunkNum := start / s.chunkSize
	startKey := make([]byte, 41)
	copy(startKey, prefix)
	binary.BigEndian.PutUint64(startKey[33:], startChunkNum)

	upperBound := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var reconstructed []uint64
	var skippedOffset uint64
	var skippedOffsetSet bool

	for valid := iter.SeekGE(startKey); valid; valid = iter.Next() {
		k := iter.Key()
		if len(k) != 41 {
			return nil, fmt.Errorf("invalid chunk key length: %d", len(k))
		}
		chunkNum := binary.BigEndian.Uint64(k[33:])
		val := iter.Value()

		r, relIndices, err := deserializeChunkValue(val)
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize chunk %d: %w", chunkNum, err)
		}

		if !skippedOffsetSet {
			skippedOffset = r.End()
			skippedOffsetSet = true
		}

		for _, rel := range relIndices {
			abs := chunkNum*s.chunkSize + uint64(rel)
			reconstructed = append(reconstructed, abs)
		}
	}

	if len(reconstructed) == 0 {
		return nil, nil
	}

	if !skippedOffsetSet {
		return nil, fmt.Errorf("no chunks found for key %x starting from chunk %d", key, startChunkNum)
	}

	if start < skippedOffset {
		return nil, fmt.Errorf("invariant violated: start %d < skippedOffset %d", start, skippedOffset)
	}

	offset := start - skippedOffset
	if offset >= uint64(len(reconstructed)) {
		return nil, nil
	}

	return reconstructed[offset:], nil
}

// GetSubRoot returns the root hash of the key's index log.
func (s *ChunkScanStore) GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error) {
	// Share identical implementation with ChunkStore GetSubRoot
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	upperBound := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return [32]byte{}, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	if iter.SeekLT(upperBound) {
		k := iter.Key()
		if bytes.HasPrefix(k, prefix) {
			val := iter.Value()
			r, relIndices, err := deserializeChunkValue(val)
			if err != nil {
				return [32]byte{}, fmt.Errorf("failed to deserialize latest chunk value for key %x: %w", key, err)
			}
			chunkNum := binary.BigEndian.Uint64(k[33:])
			for _, rel := range relIndices {
				abs := chunkNum*s.chunkSize + uint64(rel)
				var idxBytes [8]byte
				binary.BigEndian.PutUint64(idxBytes[:], abs)
				leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
				if err := r.Append(leafHash, nil); err != nil {
					return [32]byte{}, fmt.Errorf("failed to append on-the-fly leaf hash for index %d: %w", abs, err)
				}
			}
			if r.End() == 0 {
				var root [32]byte
				copy(root[:], rfc6962.DefaultHasher.EmptyRoot())
				return root, nil
			}
			rootHash, err := r.GetRootHash(nil)
			if err != nil {
				return [32]byte{}, fmt.Errorf("failed to get root hash: %w", err)
			}
			var root [32]byte
			copy(root[:], rootHash)
			return root, nil
		}
	}

	// Not found
	var root [32]byte
	copy(root[:], rfc6962.DefaultHasher.EmptyRoot())
	return root, nil
}
