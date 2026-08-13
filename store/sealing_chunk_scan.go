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

// SealingChunkScanStore implements the IndexStore interface using a chunked log construction
// in Pebble, optimized for range scans.
type SealingChunkScanStore struct {
	db        *pebble.DB
	chunkSize uint64
}

// NewSealingChunkScanStore opens a Pebble DB at the specified directory with the given chunk size.
func NewSealingChunkScanStore(dir string, chunkSize uint64) (*SealingChunkScanStore, error) {
	if chunkSize == 0 || chunkSize > 65536 {
		return nil, fmt.Errorf("invalid chunkSize %d: must be > 0 and <= 65536", chunkSize)
	}
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %w", dir, err)
	}
	return &SealingChunkScanStore{
		db:        db,
		chunkSize: chunkSize,
	}, nil
}

// Compact runs a database compaction.
func (s *SealingChunkScanStore) Compact() error {
	return s.db.Compact([]byte{0}, []byte{0xff}, false)
}

// Close flushes all writes and closes the database.
func (s *SealingChunkScanStore) Close() error {
	return s.db.Close()
}

// WriteBatch writes a batch of updates atomically.
func (s *SealingChunkScanStore) WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error {
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
		var currCumulativeCount uint64

		hasPrev := prevKey != nil
		if hasPrev {
			if len(prevKey) != 41 {
				return fmt.Errorf("invalid prevKey length: %d", len(prevKey))
			}
			prevChunkNum := binary.BigEndian.Uint64(prevKey[33:])

			pCumulativeCount, pRange, pRelIndices, err := deserializeLatestValueScanSealing(prevVal)
			if err != nil {
				return fmt.Errorf("failed to deserialize latest value for key %x, chunk %d: %w", key, prevChunkNum, err)
			}

			currChunkNum = prevChunkNum
			currCumulativeCount = pCumulativeCount
			currRange = pRange
			currRelativeIndices = pRelIndices
		} else {
			currChunkNum = newIndices[0] / s.chunkSize
			currRange = fact.NewEmptyRange(0)
			currRelativeIndices = []uint16{}
			currCumulativeCount = 0
		}

		for _, idx := range newIndices {
			chunkNum := idx / s.chunkSize
			if chunkNum != currChunkNum {
				// Reconstruct absolute indices and append leaf hashes to currRange
				for _, rel := range currRelativeIndices {
					abs := currChunkNum*s.chunkSize + uint64(rel)
					var idxBytes [8]byte
					binary.BigEndian.PutUint64(idxBytes[:], abs)
					leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
					if err := currRange.Append(leafHash, nil); err != nil {
						return fmt.Errorf("failed to append leaf hash for index %d on seal: %w", abs, err)
					}
				}

				// Seal current chunk as Older
				olderValBytes := serializeOlderValueScanSealing(currRelativeIndices)
				dbKey := make([]byte, 41)
				copy(dbKey, prefix)
				binary.BigEndian.PutUint64(dbKey[33:], currChunkNum)
				if err := batch.Set(dbKey, olderValBytes, pebble.NoSync); err != nil {
					return fmt.Errorf("failed to seal chunk %d as older: %w", currChunkNum, err)
				}

				// Set up new chunk
				currChunkNum = chunkNum
				currRelativeIndices = []uint16{}
			}

			relIdx := uint16(idx % s.chunkSize)
			currRelativeIndices = append(currRelativeIndices, relIdx)
			currCumulativeCount++
		}

		// Write final chunk as Latest
		latestValBytes := serializeLatestValueScanSealing(currCumulativeCount, currRange, currRelativeIndices)
		dbKey := make([]byte, 41)
		copy(dbKey, prefix)
		binary.BigEndian.PutUint64(dbKey[33:], currChunkNum)
		if err := batch.Set(dbKey, latestValBytes, pebble.NoSync); err != nil {
			return fmt.Errorf("failed to write latest chunk %d: %w", currChunkNum, err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}
	return nil
}

// Lookup returns all Input Log indices associated with the key,
// starting from the start-th match onwards (0-indexed).
func (s *SealingChunkScanStore) Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error) {
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

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
	var cumulativeCount uint64
	var foundLatest bool

	for valid := iter.SeekGE(startKey); valid; valid = iter.Next() {
		k := iter.Key()
		if len(k) != 41 {
			return nil, fmt.Errorf("invalid key length: %d", len(k))
		}
		chunkNum := binary.BigEndian.Uint64(k[33:])
		val := iter.Value()
		if len(val) == 0 {
			return nil, fmt.Errorf("empty value for chunk %d", chunkNum)
		}

		var relIndices []uint16
		if val[0] == flagLatest {
			cum, _, rels, err := deserializeLatestValueScanSealing(val)
			if err != nil {
				return nil, fmt.Errorf("failed to deserialize latest value for chunk %d: %w", chunkNum, err)
			}
			relIndices = rels
			cumulativeCount = cum
			foundLatest = true
		} else if val[0] == flagOlder {
			rels, err := deserializeOlderValueScanSealing(val)
			if err != nil {
				return nil, fmt.Errorf("failed to deserialize older value for chunk %d: %w", chunkNum, err)
			}
			relIndices = rels
		} else {
			return nil, fmt.Errorf("invalid flag 0x%x for chunk %d", val[0], chunkNum)
		}

		for _, rel := range relIndices {
			abs := chunkNum*s.chunkSize + uint64(rel)
			reconstructed = append(reconstructed, abs)
		}
	}

	if len(reconstructed) == 0 {
		return nil, nil
	}

	if !foundLatest {
		return nil, fmt.Errorf("latest chunk not found for key %x", key)
	}

	if cumulativeCount < uint64(len(reconstructed)) {
		return nil, fmt.Errorf("invalid cumulative count %d, less than reconstructed length %d", cumulativeCount, len(reconstructed))
	}

	skipped := cumulativeCount - uint64(len(reconstructed))
	if start < skipped {
		return nil, fmt.Errorf("invariant violated: start %d < skipped %d", start, skipped)
	}

	offset := start - skipped
	if offset >= uint64(len(reconstructed)) {
		return nil, nil
	}

	return reconstructed[offset:], nil
}

// GetSubRoot returns the root hash of the key's index log.
func (s *SealingChunkScanStore) GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error) {
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
			if len(val) == 0 {
				return [32]byte{}, fmt.Errorf("empty value for latest chunk of key %x", key)
			}
			if val[0] != flagLatest {
				return [32]byte{}, fmt.Errorf("latest chunk for key %x is not flagged as Latest: 0x%x", key, val[0])
			}
			_, r, relIndices, err := deserializeLatestValueScanSealing(val)
			if err != nil {
				return [32]byte{}, fmt.Errorf("failed to deserialize latest value for key %x: %w", key, err)
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

func serializeLatestValueScanSealing(cumulativeCount uint64, r *compact.Range, relativeIndices []uint16) []byte {
	serializedRange := SerializeRange(r)
	rangeLen := len(serializedRange)

	varintBuf := make([]byte, binary.MaxVarintLen64)
	varintLen := binary.PutUvarint(varintBuf, uint64(rangeLen))

	relBytes := serializeUint16Slice(relativeIndices)

	buf := make([]byte, 1+8+varintLen+rangeLen+len(relBytes))
	buf[0] = flagLatest
	binary.BigEndian.PutUint64(buf[1:9], cumulativeCount)
	copy(buf[9:9+varintLen], varintBuf[:varintLen])
	copy(buf[9+varintLen:9+varintLen+rangeLen], serializedRange)
	copy(buf[9+varintLen+rangeLen:], relBytes)
	return buf
}

func deserializeLatestValueScanSealing(data []byte) (uint64, *compact.Range, []uint16, error) {
	if len(data) < 9 {
		return 0, nil, nil, fmt.Errorf("data too short")
	}
	if data[0] != flagLatest {
		return 0, nil, nil, fmt.Errorf("invalid flag: expected 0x01, got 0x%x", data[0])
	}
	cumulativeCount := binary.BigEndian.Uint64(data[1:9])

	rangeLen, varintLen := binary.Uvarint(data[9:])
	if varintLen <= 0 {
		return 0, nil, nil, fmt.Errorf("invalid varint for range length")
	}

	offset := 9 + varintLen
	if len(data) < offset+int(rangeLen) {
		return 0, nil, nil, fmt.Errorf("data too short for compact range")
	}

	rangeBytes := data[offset : offset+int(rangeLen)]
	r, err := DeserializeRange(rangeBytes)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to deserialize range: %w", err)
	}

	relBytes := data[offset+int(rangeLen):]
	relIndices, err := deserializeUint16Slice(relBytes)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to deserialize relative indices: %w", err)
	}

	return cumulativeCount, r, relIndices, nil
}

func serializeOlderValueScanSealing(relativeIndices []uint16) []byte {
	relBytes := serializeUint16Slice(relativeIndices)
	buf := make([]byte, 1+len(relBytes))
	buf[0] = flagOlder
	copy(buf[1:], relBytes)
	return buf
}

func deserializeOlderValueScanSealing(data []byte) ([]uint16, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("data too short")
	}
	if data[0] != flagOlder {
		return nil, fmt.Errorf("invalid flag: expected 0x02, got 0x%x", data[0])
	}
	relIndices, err := deserializeUint16Slice(data[1:])
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize relative indices: %w", err)
	}
	return relIndices, nil
}
