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

const chunkPrefix = 'c'

// ChunkStore implements the IndexStore interface using a chunked log construction in Pebble.
type ChunkStore struct {
	db        *pebble.DB
	chunkSize uint64
}

// NewChunkStore opens a Pebble DB at the specified directory with the given chunk size.
func NewChunkStore(dir string, chunkSize uint64) (*ChunkStore, error) {
	if chunkSize == 0 || chunkSize > 65536 {
		return nil, fmt.Errorf("invalid chunkSize %d: must be > 0 and <= 65536", chunkSize)
	}
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %w", dir, err)
	}
	return &ChunkStore{
		db:        db,
		chunkSize: chunkSize,
	}, nil
}

// Compact runs a database compaction.
func (s *ChunkStore) Compact() error {
	return s.db.Compact([]byte{0}, []byte{0xff}, false)
}

// Close flushes all writes and closes the database.
func (s *ChunkStore) Close() error {
	return s.db.Close()
}

// WriteBatch writes a batch of updates atomically.
func (s *ChunkStore) WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error {
	batch := s.db.NewBatch()
	defer batch.Close()

	for key, newIndices := range updates {
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

		// Find previous latest block
		upperBound := prefixUpperBound(prefix)
		iter, err := s.db.NewIter(&pebble.IterOptions{})
		if err != nil {
			return fmt.Errorf("failed to create iterator: %w", err)
		}

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
		if err := iter.Close(); err != nil {
			return fmt.Errorf("failed to close iterator: %w", err)
		}

		var currBlockNum uint64
		var currPrevBlockNum uint64
		var currRange *compact.Range
		var currRelativeIndices []uint16
		var currCumulativeCount uint64

		hasPrev := prevKey != nil
		if hasPrev {
			if len(prevKey) != 41 {
				return fmt.Errorf("invalid prevKey length: %d", len(prevKey))
			}
			prevBlockNum := binary.BigEndian.Uint64(prevKey[33:])

			pPrevBlockNum, pCumulativeCount, pRange, pRelIndices, err := deserializeLatestValue(prevVal)
			if err != nil {
				return fmt.Errorf("failed to deserialize latest value for key %x, block %d: %w", key, prevBlockNum, err)
			}

			currBlockNum = prevBlockNum
			currPrevBlockNum = pPrevBlockNum
			currCumulativeCount = pCumulativeCount
			currRange = pRange
			currRelativeIndices = pRelIndices
		} else {
			currBlockNum = newIndices[0] / s.chunkSize
			currPrevBlockNum = 0
			currRange = fact.NewEmptyRange(0)
			currRelativeIndices = []uint16{}
			currCumulativeCount = 0
		}

		for _, idx := range newIndices {
			blockNum := idx / s.chunkSize
			if blockNum != currBlockNum {
				// Reconstruct absolute indices and append leaf hashes to currRange
				for _, rel := range currRelativeIndices {
					abs := currBlockNum*s.chunkSize + uint64(rel)
					var idxBytes [8]byte
					binary.BigEndian.PutUint64(idxBytes[:], abs)
					leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
					if err := currRange.Append(leafHash, nil); err != nil {
						return fmt.Errorf("failed to append leaf hash for index %d on seal: %w", abs, err)
					}
				}

				// Seal current block as Older
				olderValBytes := serializeOlderValue(currPrevBlockNum, currCumulativeCount, currRelativeIndices)
				dbKey := make([]byte, 41)
				copy(dbKey, prefix)
				binary.BigEndian.PutUint64(dbKey[33:], currBlockNum)
				if err := batch.Set(dbKey, olderValBytes, pebble.NoSync); err != nil {
					return fmt.Errorf("failed to seal block %d as older: %w", currBlockNum, err)
				}

				// Set up new block
				currPrevBlockNum = currBlockNum
				currBlockNum = blockNum
				currRelativeIndices = []uint16{}
			}

			relIdx := uint16(idx % s.chunkSize)
			currRelativeIndices = append(currRelativeIndices, relIdx)
			currCumulativeCount++
		}

		// Write final block as Latest
		latestValBytes := serializeLatestValue(currPrevBlockNum, currCumulativeCount, currRange, currRelativeIndices)
		dbKey := make([]byte, 41)
		copy(dbKey, prefix)
		binary.BigEndian.PutUint64(dbKey[33:], currBlockNum)
		if err := batch.Set(dbKey, latestValBytes, pebble.NoSync); err != nil {
			return fmt.Errorf("failed to write latest block %d: %w", currBlockNum, err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}
	return nil
}

// Lookup returns all Input Log indices associated with the key,
// starting from the start-th match onwards (0-indexed).
func (s *ChunkStore) Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error) {
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	// Find the latest block
	upperBound := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var latestKey []byte
	var latestVal []byte

	if iter.SeekLT(upperBound) {
		k := iter.Key()
		if bytes.HasPrefix(k, prefix) {
			latestKey = make([]byte, len(k))
			copy(latestKey, k)
			latestVal = make([]byte, len(iter.Value()))
			copy(latestVal, iter.Value())
		}
	}

	if latestKey == nil {
		return nil, nil
	}

	if len(latestKey) != 41 {
		return nil, fmt.Errorf("invalid latestKey length: %d", len(latestKey))
	}
	latestBlockNum := binary.BigEndian.Uint64(latestKey[33:])

	prevBlockNum, cumulativeCount, _, relIndices, err := deserializeLatestValue(latestVal)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize latest value: %w", err)
	}

	type blockData struct {
		blockNum        uint64
		rel             []uint16
		cumulativeCount uint64
	}
	var blocks []blockData
	blocks = append(blocks, blockData{
		blockNum:        latestBlockNum,
		rel:             relIndices,
		cumulativeCount: cumulativeCount,
	})

	currPrevBlockNum := prevBlockNum
	currCumulativeCount := cumulativeCount
	currRelIndicesLen := uint64(len(relIndices))

	for currCumulativeCount-currRelIndicesLen > start {
		// We need to traverse to currPrevBlockNum
		prevKey := make([]byte, 41)
		copy(prevKey, prefix)
		binary.BigEndian.PutUint64(prevKey[33:], currPrevBlockNum)

		val, closer, err := s.db.Get(prevKey)
		if err != nil {
			return nil, fmt.Errorf("failed to get block %d: %w", currPrevBlockNum, err)
		}

		pPrevBlockNum, pCumulativeCount, pRelIndices, err := deserializeOlderValue(val)
		if err := closer.Close(); err != nil {
			return nil, fmt.Errorf("failed to close db val closer: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize older value for block %d: %w", currPrevBlockNum, err)
		}

		blocks = append(blocks, blockData{
			blockNum:        currPrevBlockNum,
			rel:             pRelIndices,
			cumulativeCount: pCumulativeCount,
		})

		currPrevBlockNum = pPrevBlockNum
		currCumulativeCount = pCumulativeCount
		currRelIndicesLen = uint64(len(pRelIndices))
	}

	// Now reconstruct absolute indices from blocks, in forward order
	var indices []uint64
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		for _, rel := range b.rel {
			abs := b.blockNum*s.chunkSize + uint64(rel)
			indices = append(indices, abs)
		}
	}

	oldestBlock := blocks[len(blocks)-1]
	if oldestBlock.cumulativeCount < uint64(len(oldestBlock.rel)) {
		return nil, fmt.Errorf("database corruption: oldest block cumulative count %d is less than block relative indices length %d", oldestBlock.cumulativeCount, len(oldestBlock.rel))
	}
	startOffsetInOldest := oldestBlock.cumulativeCount - uint64(len(oldestBlock.rel))
	if start < startOffsetInOldest {
		return nil, fmt.Errorf("invariant violated: start %d is less than startOffsetInOldest %d", start, startOffsetInOldest)
	}
	offset := start - startOffsetInOldest

	if offset >= uint64(len(indices)) {
		return nil, nil
	}
	return indices[offset:], nil
}

// GetSubRoot returns the root hash of the key's index log.
func (s *ChunkStore) GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error) {
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
			_, _, r, relIndices, err := deserializeLatestValue(val)
			if err != nil {
				return [32]byte{}, fmt.Errorf("failed to deserialize latest value for key %x: %w", key, err)
			}
			blockNum := binary.BigEndian.Uint64(k[33:])
			for _, rel := range relIndices {
				abs := blockNum*s.chunkSize + uint64(rel)
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

func serializeUint16Slice(slice []uint16) []byte {
	buf := make([]byte, 2*len(slice))
	for i, v := range slice {
		binary.BigEndian.PutUint16(buf[2*i:2*i+2], v)
	}
	return buf
}

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

func serializeLatestValue(prevBlockNum uint64, cumulativeCount uint64, r *compact.Range, relativeIndices []uint16) []byte {
	serializedRange := SerializeRange(r)
	rangeLen := len(serializedRange)

	varintBuf := make([]byte, binary.MaxVarintLen64)
	varintLen := binary.PutUvarint(varintBuf, uint64(rangeLen))

	relBytes := serializeUint16Slice(relativeIndices)

	buf := make([]byte, 1+8+8+varintLen+rangeLen+len(relBytes))
	buf[0] = flagLatest
	binary.BigEndian.PutUint64(buf[1:9], prevBlockNum)
	binary.BigEndian.PutUint64(buf[9:17], cumulativeCount)
	copy(buf[17:17+varintLen], varintBuf[:varintLen])
	copy(buf[17+varintLen:17+varintLen+rangeLen], serializedRange)
	copy(buf[17+varintLen+rangeLen:], relBytes)
	return buf
}

func deserializeLatestValue(data []byte) (uint64, uint64, *compact.Range, []uint16, error) {
	if len(data) < 17 {
		return 0, 0, nil, nil, fmt.Errorf("data too short")
	}
	if data[0] != flagLatest {
		return 0, 0, nil, nil, fmt.Errorf("invalid flag: expected 0x01, got 0x%x", data[0])
	}
	prevBlockNum := binary.BigEndian.Uint64(data[1:9])
	cumulativeCount := binary.BigEndian.Uint64(data[9:17])

	rangeLen, varintLen := binary.Uvarint(data[17:])
	if varintLen <= 0 {
		return 0, 0, nil, nil, fmt.Errorf("invalid varint for range length")
	}

	offset := 17 + varintLen
	if len(data) < offset+int(rangeLen) {
		return 0, 0, nil, nil, fmt.Errorf("data too short for compact range")
	}

	rangeBytes := data[offset : offset+int(rangeLen)]
	r, err := DeserializeRange(rangeBytes)
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("failed to deserialize range: %w", err)
	}

	relBytes := data[offset+int(rangeLen):]
	relIndices, err := deserializeUint16Slice(relBytes)
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("failed to deserialize relative indices: %w", err)
	}

	return prevBlockNum, cumulativeCount, r, relIndices, nil
}

func serializeOlderValue(prevBlockNum uint64, cumulativeCount uint64, relativeIndices []uint16) []byte {
	relBytes := serializeUint16Slice(relativeIndices)
	buf := make([]byte, 1+8+8+len(relBytes))
	buf[0] = flagOlder
	binary.BigEndian.PutUint64(buf[1:9], prevBlockNum)
	binary.BigEndian.PutUint64(buf[9:17], cumulativeCount)
	copy(buf[17:], relBytes)
	return buf
}

func deserializeOlderValue(data []byte) (uint64, uint64, []uint16, error) {
	if len(data) < 17 {
		return 0, 0, nil, fmt.Errorf("data too short")
	}
	if data[0] != flagOlder {
		return 0, 0, nil, fmt.Errorf("invalid flag: expected 0x02, got 0x%x", data[0])
	}
	prevBlockNum := binary.BigEndian.Uint64(data[1:9])
	cumulativeCount := binary.BigEndian.Uint64(data[9:17])
	relIndices, err := deserializeUint16Slice(data[17:])
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to deserialize relative indices: %w", err)
	}
	return prevBlockNum, cumulativeCount, relIndices, nil
}
