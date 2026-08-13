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

const logPrefix = 'l'
const flagLatest = 0x01
const flagOlder = 0x02

// LogStore implements the IndexStore interface using a log construction in Pebble.
type LogStore struct {
	db *pebble.DB
}

// NewLogStore opens a Pebble DB at the specified directory.
func NewLogStore(dir string) (*LogStore, error) {
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %w", dir, err)
	}
	return &LogStore{db: db}, nil
}

// prefixUpperBound returns the successor of the prefix, used for SeekLT.
func prefixUpperBound(prefix []byte) []byte {
	limit := make([]byte, len(prefix))
	copy(limit, prefix)
	for i := len(limit) - 1; i >= 0; i-- {
		limit[i]++
		if limit[i] != 0 {
			return limit[:i+1]
		}
	}
	return nil
}

// WriteBatch writes a batch of updates atomically.
func (s *LogStore) WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error {
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

		prefix := make([]byte, 33)
		prefix[0] = logPrefix
		copy(prefix[1:], key[:])

		// Find previous latest key
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

		var r *compact.Range
		if prevKey != nil {
			// Found previous latest key, parse it
			var err error
			r, err = deserializeLatest(prevVal)
			if err != nil {
				return fmt.Errorf("failed to deserialize latest value for key %x: %w", key, err)
			}
			// Mark previous as older
			if err := batch.Set(prevKey, []byte{flagOlder}, pebble.NoSync); err != nil {
				return fmt.Errorf("failed to update previous latest key %x: %w", prevKey, err)
			}
		} else {
			r = fact.NewEmptyRange(0)
		}

		// Write intermediate indices
		for i := 0; i < len(newIndices)-1; i++ {
			idx := newIndices[i]
			var idxBytes [8]byte
			binary.BigEndian.PutUint64(idxBytes[:], idx)
			leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
			if err := r.Append(leafHash, nil); err != nil {
				return fmt.Errorf("failed to append leaf hash for index %d to range for key %x: %w", idx, key, err)
			}

			dbKey := make([]byte, 41)
			copy(dbKey, prefix)
			binary.BigEndian.PutUint64(dbKey[33:], idx)
			if err := batch.Set(dbKey, []byte{flagOlder}, pebble.NoSync); err != nil {
				return fmt.Errorf("failed to set intermediate key: %w", err)
			}
		}

		// Write last index as latest
		lastIdx := newIndices[len(newIndices)-1]
		var idxBytes [8]byte
		binary.BigEndian.PutUint64(idxBytes[:], lastIdx)
		leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
		if err := r.Append(leafHash, nil); err != nil {
			return fmt.Errorf("failed to append leaf hash for index %d to range for key %x: %w", lastIdx, key, err)
		}

		dbKey := make([]byte, 41)
		copy(dbKey, prefix)
		binary.BigEndian.PutUint64(dbKey[33:], lastIdx)

		valBytes := serializeLatest(r)
		if err := batch.Set(dbKey, valBytes, pebble.NoSync); err != nil {
			return fmt.Errorf("failed to set latest key: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}
	return nil
}

// Lookup returns all Input Log indices associated with the key,
// starting from the start-th match onwards (0-indexed).
func (s *LogStore) Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error) {
	prefix := make([]byte, 33)
	prefix[0] = logPrefix
	copy(prefix[1:], key[:])

	upperBound := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer iter.Close()

	var indices []uint64
	for valid := iter.SeekGE(prefix); valid; valid = iter.Next() {
		k := iter.Key()
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(k) != 41 {
			return nil, fmt.Errorf("invalid key length: %d", len(k))
		}
		idx := binary.BigEndian.Uint64(k[33:])
		indices = append(indices, idx)
	}

	if start >= uint64(len(indices)) {
		return nil, nil
	}
	return indices[start:], nil
}

// GetSubRoot returns the root hash of the key's index log.
func (s *LogStore) GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error) {
	prefix := make([]byte, 33)
	prefix[0] = logPrefix
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
			r, err := deserializeLatest(val)
			if err != nil {
				return [32]byte{}, fmt.Errorf("failed to deserialize latest value for key %x: %w", key, err)
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

// Compact runs a database compaction.
func (s *LogStore) Compact() error {
	return s.db.Compact([]byte{0}, []byte{0xff}, false)
}

// Close flushes all writes and closes the database.
func (s *LogStore) Close() error {
	return s.db.Close()
}

func serializeLatest(r *compact.Range) []byte {
	serializedRange := SerializeRange(r)
	rangeLen := len(serializedRange)

	varintBuf := make([]byte, binary.MaxVarintLen64)
	varintLen := binary.PutUvarint(varintBuf, uint64(rangeLen))

	buf := make([]byte, 1+varintLen+rangeLen)
	buf[0] = flagLatest
	copy(buf[1:1+varintLen], varintBuf[:varintLen])
	copy(buf[1+varintLen:], serializedRange)
	return buf
}

func deserializeLatest(data []byte) (*compact.Range, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("data too short")
	}
	if data[0] != flagLatest {
		return nil, fmt.Errorf("invalid flag: expected 0x01, got 0x%x", data[0])
	}
	rangeLen, varintLen := binary.Uvarint(data[1:])
	if varintLen <= 0 {
		return nil, fmt.Errorf("invalid varint for range length")
	}
	if len(data) < 1+varintLen+int(rangeLen) {
		return nil, fmt.Errorf("data too short for compact range (len %d, expected %d)", len(data), 1+varintLen+int(rangeLen))
	}
	rangeBytes := data[1+varintLen : 1+varintLen+int(rangeLen)]
	return DeserializeRange(rangeBytes)
}
