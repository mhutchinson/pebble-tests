package store

import (
	"context"
)

// IndexStore defines the interface for the Pebble storage engines.
type IndexStore interface {
	// WriteBatch writes a batch of updates atomically.
	// The map keys are the 32-byte key hashes.
	// The slice values are the new Input Log indices for that key, sorted ascending.
	WriteBatch(ctx context.Context, updates map[[32]byte][]uint64) error

	// Lookup returns all Input Log indices associated with the key,
	// starting from the start-th match onwards (0-indexed).
	Lookup(ctx context.Context, key [32]byte, start uint64) ([]uint64, error)

	// GetSubRoot returns the root hash of the key's index log.
	GetSubRoot(ctx context.Context, key [32]byte) ([32]byte, error)

	// Compact runs a database compaction.
	Compact() error

	// Close flushes all writes and closes the database.
	Close() error
}
