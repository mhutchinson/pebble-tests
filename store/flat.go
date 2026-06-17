package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
	"github.com/transparency-dev/merkle/compact"
	"github.com/transparency-dev/merkle/rfc6962"
)

const flatPrefix = 'f'

// FlatStore implements the IndexStore interface using a flat list representation in Pebble.
type FlatStore struct {
	db *pebble.DB
}

// NewFlatStore opens a Pebble DB at the specified directory.
func NewFlatStore(dir string) (*FlatStore, error) {
	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open Pebble DB at %s: %w", dir, err)
	}
	return &FlatStore{db: db}, nil
}

// WriteBatch writes a batch of updates atomically.
func (s *FlatStore) WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error {
	batch := s.db.NewBatch()
	defer batch.Close()

	for key, newIndices := range updates {
		dbKey := make([]byte, 33)
		dbKey[0] = flatPrefix
		copy(dbKey[1:], key[:])

		var r *compact.Range
		var indices []uint64

		valBytes, closer, err := s.db.Get(dbKey)
		if err != nil {
			if !errors.Is(err, pebble.ErrNotFound) {
				return fmt.Errorf("failed to read key %x: %w", key, err)
			}
			// Key not found, initialize new range and indices
			r = fact.NewEmptyRange(0)
			indices = make([]uint64, 0, len(newIndices))
		} else {
			// Key exists, deserialize range and indices
			r, indices, err = deserializeValue(valBytes)
			if err != nil {
				closer.Close()
				return fmt.Errorf("failed to deserialize value for key %x: %w", key, err)
			}
			closer.Close()
		}

		// Append new indices
		for _, idx := range newIndices {
			var idxBytes [8]byte
			binary.BigEndian.PutUint64(idxBytes[:], idx)
			leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
			if err := r.Append(leafHash, nil); err != nil {
				return fmt.Errorf("failed to append leaf hash for index %d to range for key %x: %w", idx, key, err)
			}
			indices = append(indices, idx)
		}

		// Serialize and write to batch
		newValBytes := serializeValue(r, indices)
		if err := batch.Set(dbKey, newValBytes, pebble.NoSync); err != nil {
			return fmt.Errorf("failed to set batch update for key %x: %w", key, err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}
	return nil
}

// Lookup returns all Input Log indices associated with the key,
// starting from the start-th match onwards (0-indexed).
func (s *FlatStore) Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error) {
	dbKey := make([]byte, 33)
	dbKey[0] = flatPrefix
	copy(dbKey[1:], key[:])

	valBytes, closer, err := s.db.Get(dbKey)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to lookup key %x: %w", key, err)
	}
	defer closer.Close()

	_, indices, err := deserializeValue(valBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize value for key %x: %w", key, err)
	}

	if start >= uint64(len(indices)) {
		return nil, nil
	}
	return indices[start:], nil
}

// GetSubRoot returns the root hash of the key's index log.
func (s *FlatStore) GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error) {
	dbKey := make([]byte, 33)
	dbKey[0] = flatPrefix
	copy(dbKey[1:], key[:])

	valBytes, closer, err := s.db.Get(dbKey)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			var root [32]byte
			copy(root[:], rfc6962.DefaultHasher.EmptyRoot())
			return root, nil
		}
		return [32]byte{}, fmt.Errorf("failed to get sub-root for key %x: %w", key, err)
	}
	defer closer.Close()

	r, _, err := deserializeValue(valBytes)
	if err != nil {
		return [32]byte{}, fmt.Errorf("failed to deserialize value for key %x: %w", key, err)
	}

	if r.End() == 0 {
		var root [32]byte
		copy(root[:], rfc6962.DefaultHasher.EmptyRoot())
		return root, nil
	}

	rootHash, err := r.GetRootHash(nil)
	if err != nil {
		return [32]byte{}, fmt.Errorf("failed to compute root hash for key %x: %w", key, err)
	}

	if len(rootHash) != 32 {
		return [32]byte{}, fmt.Errorf("root hash length is %d, expected 32 for key %x", len(rootHash), key)
	}

	var root [32]byte
	copy(root[:], rootHash)
	return root, nil
}

// Compact runs a database compaction.
func (s *FlatStore) Compact() error {
	return s.db.Compact([]byte{0}, []byte{0xff}, false)
}

// Close flushes all writes and closes the database.
func (s *FlatStore) Close() error {
	return s.db.Close()
}

// serializeValue serializes a compact.Range and []uint64 slice.
func serializeValue(r *compact.Range, indices []uint64) []byte {
	serializedRange := SerializeRange(r)
	rangeLen := len(serializedRange)

	// Varint for rangeLen
	varintBuf := make([]byte, binary.MaxVarintLen64)
	varintLen := binary.PutUvarint(varintBuf, uint64(rangeLen))

	indicesBuf := make([]byte, len(indices)*8)
	for i, idx := range indices {
		binary.BigEndian.PutUint64(indicesBuf[i*8:(i+1)*8], idx)
	}

	buf := make([]byte, varintLen+rangeLen+len(indicesBuf))
	copy(buf[0:varintLen], varintBuf[:varintLen])
	copy(buf[varintLen:varintLen+rangeLen], serializedRange)
	copy(buf[varintLen+rangeLen:], indicesBuf)
	return buf
}

// deserializeValue deserializes a compact.Range and []uint64 slice.
func deserializeValue(data []byte) (*compact.Range, []uint64, error) {
	rangeLen, varintLen := binary.Uvarint(data)
	if varintLen <= 0 {
		return nil, nil, fmt.Errorf("invalid varint for range length")
	}

	if len(data) < varintLen+int(rangeLen) {
		return nil, nil, fmt.Errorf("data too short for compact range (length %d, expected %d)", len(data), varintLen+int(rangeLen))
	}

	rangeBytes := data[varintLen : varintLen+int(rangeLen)]
	r, err := DeserializeRange(rangeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to deserialize range: %w", err)
	}

	indicesBytes := data[varintLen+int(rangeLen):]
	if len(indicesBytes)%8 != 0 {
		return nil, nil, fmt.Errorf("invalid indices bytes length: %d (must be a multiple of 8)", len(indicesBytes))
	}

	numIndices := len(indicesBytes) / 8
	indices := make([]uint64, numIndices)
	for i := 0; i < numIndices; i++ {
		indices[i] = binary.BigEndian.Uint64(indicesBytes[i*8 : (i+1)*8])
	}

	return r, indices, nil
}
