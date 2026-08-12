package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"testing"

	"github.com/transparency-dev/merkle/rfc6962"
)

func TestFlatStoreCorrectness(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-flat-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewFlatStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create FlatStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestLogStoreCorrectness(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-log-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewLogStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create LogStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func runIndexStoreTests(t *testing.T, s IndexStore) {
	ctx := context.Background()
	key1 := sha256.Sum256([]byte("key1"))
	key2 := sha256.Sum256([]byte("key2"))

	// Write first batch
	updates1 := map[[32]byte][]uint64{
		key1: {1, 5, 10},
		key2: {2},
	}
	if err := s.WriteBatch(ctx, updates1); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify lookups
	got1, err := s.Lookup(ctx, key1, 0)
	if err != nil {
		t.Errorf("Lookup key1 failed: %v", err)
	}
	if want := []uint64{1, 5, 10}; !equalSlices(got1, want) {
		t.Errorf("Lookup key1: got %v, want %v", got1, want)
	}

	// Verify lookup with start offset
	got1Start, err := s.Lookup(ctx, key1, 1)
	if err != nil {
		t.Errorf("Lookup key1 from 1 failed: %v", err)
	}
	if want := []uint64{5, 10}; !equalSlices(got1Start, want) {
		t.Errorf("Lookup key1 from 1: got %v, want %v", got1Start, want)
	}

	// Verify lookup out of bounds
	got1OutOfBounds, err := s.Lookup(ctx, key1, 5)
	if err != nil {
		t.Errorf("Lookup key1 out of bounds failed: %v", err)
	}
	if len(got1OutOfBounds) != 0 {
		t.Errorf("Lookup key1 out of bounds: got %v, want empty", got1OutOfBounds)
	}

	// Write second batch (appending to key1 and key2)
	updates2 := map[[32]byte][]uint64{
		key1: {20, 25},
		key2: {4, 6},
	}
	if err := s.WriteBatch(ctx, updates2); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify combined updates
	got1All, err := s.Lookup(ctx, key1, 0)
	if err != nil {
		t.Errorf("Lookup key1 failed: %v", err)
	}
	if want := []uint64{1, 5, 10, 20, 25}; !equalSlices(got1All, want) {
		t.Errorf("Lookup key1: got %v, want %v", got1All, want)
	}

	// Verify sub-root
	subRoot1, err := s.GetSubRoot(ctx, key1)
	if err != nil {
		t.Errorf("GetSubRoot key1 failed: %v", err)
	}
	if subRoot1 == [32]byte{} {
		t.Errorf("GetSubRoot key1 returned zero hash")
	}

	// Test writing empty batch updates
	keyEmpty := sha256.Sum256([]byte("empty-key"))
	updatesEmpty := map[[32]byte][]uint64{
		keyEmpty: {},
	}
	if err := s.WriteBatch(ctx, updatesEmpty); err != nil {
		t.Fatalf("WriteBatch with empty updates failed: %v", err)
	}

	// Lookup keyEmpty
	gotEmpty, err := s.Lookup(ctx, keyEmpty, 0)
	if err != nil {
		t.Errorf("Lookup keyEmpty failed: %v", err)
	}
	if len(gotEmpty) != 0 {
		t.Errorf("Lookup keyEmpty: got %v, want empty", gotEmpty)
	}

	// GetSubRoot of keyEmpty (should be the empty Merkle tree root, not zero hash)
	subRootEmpty, err := s.GetSubRoot(ctx, keyEmpty)
	if err != nil {
		t.Errorf("GetSubRoot keyEmpty failed: %v", err)
	}
	if subRootEmpty == [32]byte{} {
		t.Errorf("GetSubRoot keyEmpty returned zero hash, expected empty tree root")
	}

	// Key that does not exist
	key3 := sha256.Sum256([]byte("non-existent"))
	got3, err := s.Lookup(ctx, key3, 0)
	if err != nil {
		t.Errorf("Lookup key3 failed: %v", err)
	}
	if got3 != nil {
		t.Errorf("Lookup key3: got %v, want nil", got3)
	}

	subRoot3, err := s.GetSubRoot(ctx, key3)
	if err != nil {
		t.Errorf("GetSubRoot key3 failed: %v", err)
	}
	wantEmptyRoot := [32]byte{}
	copy(wantEmptyRoot[:], rfc6962.DefaultHasher.EmptyRoot())
	if subRoot3 != wantEmptyRoot {
		t.Errorf("GetSubRoot key3: got %x, want empty root %x", subRoot3, wantEmptyRoot)
	}
}

func TestLogStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-log-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewLogStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create LogStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 20
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Write second batch: index 30
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {30}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Construct keys
	prefix := make([]byte, 33)
	prefix[0] = logPrefix
	copy(prefix[1:], key[:])

	key10 := make([]byte, 41)
	copy(key10, prefix)
	binary.BigEndian.PutUint64(key10[33:], 10)

	key20 := make([]byte, 41)
	copy(key20, prefix)
	binary.BigEndian.PutUint64(key20[33:], 20)

	key30 := make([]byte, 41)
	copy(key30, prefix)
	binary.BigEndian.PutUint64(key30[33:], 30)

	// Helper to get raw value
	getRaw := func(k []byte) []byte {
		val, closer, err := s.db.Get(k)
		if err != nil {
			t.Fatalf("Failed to get raw key %x: %v", k, err)
		}
		defer closer.Close()
		ret := make([]byte, len(val))
		copy(ret, val)
		return ret
	}

	val10 := getRaw(key10)
	if !bytes.Equal(val10, []byte{flagOlder}) {
		t.Errorf("Expected key 10 to have value 0x02, got %x", val10)
	}

	val20 := getRaw(key20)
	if !bytes.Equal(val20, []byte{flagOlder}) {
		t.Errorf("Expected key 20 to have value 0x02, got %x", val20)
	}

	val30 := getRaw(key30)
	if len(val30) < 1 || val30[0] != flagLatest {
		t.Errorf("Expected key 30 to start with 0x01, got %x", val30)
	}
}

func TestSerializeDeserializeValue(t *testing.T) {
	// Build a non-empty range
	r := fact.NewEmptyRange(0)
	for i := uint64(0); i < 5; i++ {
		var idxBytes [8]byte
		binary.BigEndian.PutUint64(idxBytes[:], i*10)
		leafHash := rfc6962.DefaultHasher.HashLeaf(idxBytes[:])
		if err := r.Append(leafHash, nil); err != nil {
			t.Fatalf("Failed to append: %v", err)
		}
	}
	indices := []uint64{0, 10, 20, 30, 40}

	data := serializeValue(r, indices)

	r2, indices2, err := deserializeValue(data)
	if err != nil {
		t.Fatalf("Failed to deserialize: %v", err)
	}

	if r2.End() != r.End() {
		t.Errorf("Range end mismatch: got %d, want %d", r2.End(), r.End())
	}
	if !equalSlices(indices2, indices) {
		t.Errorf("Indices mismatch: got %v, want %v", indices2, indices)
	}
}

func TestDeserializeCorruptValue(t *testing.T) {
	// Too short for varint
	if _, _, err := deserializeValue([]byte{}); err == nil {
		t.Error("Expected error for empty bytes")
	}

	// Varint indicates range size is larger than data
	varintBuf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(varintBuf, 10)
	data := append(varintBuf[:n], []byte{1, 2, 3, 4, 5}...)
	if _, _, err := deserializeValue(data); err == nil {
		t.Error("Expected error for data shorter than indicated range size")
	}

	// Range data length mismatch
	rangeData := make([]byte, 8)
	binary.BigEndian.PutUint64(rangeData, 1) // says size is 1, needs 40 bytes.
	data2 := make([]byte, binary.MaxVarintLen64)
	n2 := binary.PutUvarint(data2, 8)
	data2 = append(data2[:n2], rangeData...)
	if _, _, err := deserializeValue(data2); err == nil {
		t.Error("Expected error for corrupt range data (length mismatch)")
	}

	// Indices bytes length is not a multiple of 8
	validRangeData := make([]byte, 8)
	binary.BigEndian.PutUint64(validRangeData, 0)
	data3 := make([]byte, binary.MaxVarintLen64)
	n3 := binary.PutUvarint(data3, 8)
	data3 = append(data3[:n3], validRangeData...)
	data3 = append(data3, []byte{1, 2, 3, 4, 5}...)
	if _, _, err := deserializeValue(data3); err == nil {
		t.Error("Expected error for indices bytes not multiple of 8")
	}
}

func equalSlices(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChunkStoreCorrectness_Size2(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-test-2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create ChunkStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestChunkStoreCorrectness_Size1024(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-test-1024-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkStore(tmpDir, 1024)
	if err != nil {
		t.Fatalf("Failed to create ChunkStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestChunkStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create ChunkStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 11 (both block 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify block 5 is latest
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyBlock5 := make([]byte, 41)
	copy(keyBlock5, prefix)
	binary.BigEndian.PutUint64(keyBlock5[33:], 5)

	getRaw := func(k []byte) []byte {
		val, closer, err := s.db.Get(k)
		if err != nil {
			t.Fatalf("Failed to get raw key %x: %v", k, err)
		}
		defer closer.Close()
		ret := make([]byte, len(val))
		copy(ret, val)
		return ret
	}

	val5 := getRaw(keyBlock5)
	if len(val5) < 17 {
		t.Fatalf("Value too short: %d", len(val5))
	}
	if val5[0] != flagLatest {
		t.Errorf("Expected block 5 to be latest (0x01), got %x", val5[0])
	}
	prev5 := binary.BigEndian.Uint64(val5[1:9])
	if prev5 != 0 {
		t.Errorf("Expected block 5 prev to be 0, got %d", prev5)
	}
	cum5 := binary.BigEndian.Uint64(val5[9:17])
	if cum5 != 2 {
		t.Errorf("Expected block 5 cumulative count to be 2, got %d", cum5)
	}

	// Write second batch: index 20 (block 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify block 5 is now older (0x02)
	val5Older := getRaw(keyBlock5)
	if len(val5Older) < 17 {
		t.Fatalf("Value too short: %d", len(val5Older))
	}
	if val5Older[0] != flagOlder {
		t.Errorf("Expected block 5 to be older (0x02), got %x", val5Older[0])
	}
	prev5Older := binary.BigEndian.Uint64(val5Older[1:9])
	if prev5Older != 0 {
		t.Errorf("Expected block 5 older prev to be 0, got %d", prev5Older)
	}
	cum5Older := binary.BigEndian.Uint64(val5Older[9:17])
	if cum5Older != 2 {
		t.Errorf("Expected block 5 older cumulative count to be 2, got %d", cum5Older)
	}

	localDeserializeUint16Slice := func(buf []byte) []uint16 {
		slice := make([]uint16, len(buf)/2)
		for i := 0; i < len(slice); i++ {
			slice[i] = binary.BigEndian.Uint16(buf[2*i : 2*i+2])
		}
		return slice
	}

	rel5Older := localDeserializeUint16Slice(val5Older[17:])
	if !equalUint16Slices(rel5Older, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5Older)
	}

	// Verify block 10 is latest (0x01)
	keyBlock10 := make([]byte, 41)
	copy(keyBlock10, prefix)
	binary.BigEndian.PutUint64(keyBlock10[33:], 10)

	val10 := getRaw(keyBlock10)
	if len(val10) < 17 {
		t.Fatalf("Value too short: %d", len(val10))
	}
	if val10[0] != flagLatest {
		t.Errorf("Expected block 10 to be latest (0x01), got %x", val10[0])
	}
	prev10 := binary.BigEndian.Uint64(val10[1:9])
	if prev10 != 5 {
		t.Errorf("Expected block 10 prev to be 5, got %d", prev10)
	}
	cum10 := binary.BigEndian.Uint64(val10[9:17])
	if cum10 != 3 {
		t.Errorf("Expected block 10 cumulative count to be 3, got %d", cum10)
	}
}

func equalUint16Slices(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewChunkStoreInvalidChunkSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-invalid-size-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := NewChunkStore(tmpDir, 0); err == nil {
		t.Error("Expected error for chunkSize = 0")
	}

	if _, err := NewChunkStore(tmpDir, 65537); err == nil {
		t.Error("Expected error for chunkSize = 65537")
	}

	// Boundary check
	s1, err := NewChunkStore(tmpDir, 1)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 1: %v", err)
	} else {
		s1.Close()
	}

	s2, err := NewChunkStore(tmpDir, 65536)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 65536: %v", err)
	} else {
		s2.Close()
	}
}

func TestChunkScanStoreCorrectness_Size2(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-scan-test-2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create ChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestChunkScanStoreCorrectness_Size1024(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-scan-test-1024-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkScanStore(tmpDir, 1024)
	if err != nil {
		t.Fatalf("Failed to create ChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestChunkScanStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-scan-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create ChunkScanStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 11 (both block 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify block 5 is latest
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyBlock5 := make([]byte, 41)
	copy(keyBlock5, prefix)
	binary.BigEndian.PutUint64(keyBlock5[33:], 5)

	getRaw := func(k []byte) []byte {
		val, closer, err := s.db.Get(k)
		if err != nil {
			t.Fatalf("Failed to get raw key %x: %v", k, err)
		}
		defer closer.Close()
		ret := make([]byte, len(val))
		copy(ret, val)
		return ret
	}

	val5 := getRaw(keyBlock5)
	if len(val5) < 9 {
		t.Fatalf("Value too short: %d", len(val5))
	}
	if val5[0] != flagLatest {
		t.Errorf("Expected block 5 to be latest (0x01), got %x", val5[0])
	}
	cum5 := binary.BigEndian.Uint64(val5[1:9])
	if cum5 != 2 {
		t.Errorf("Expected block 5 cumulative count to be 2, got %d", cum5)
	}

	// Range and rel indices check
	_, range5, rel5, err := deserializeLatestValueScan(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for block 5: %v", err)
	}
	if range5.End() != 2 {
		t.Errorf("Expected range end to be 2, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (block 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify block 5 is now older (0x02)
	val5Older := getRaw(keyBlock5)
	if len(val5Older) == 0 {
		t.Fatalf("Value is empty")
	}
	if val5Older[0] != flagOlder {
		t.Errorf("Expected block 5 to be older (0x02), got %x", val5Older[0])
	}
	// Older value schema: 0x02 (1B) + []uint16 (relative indices)
	// For chunkSize=2, rel indices [0, 1] takes 4 bytes. Total length = 5.
	if len(val5Older) != 5 {
		t.Errorf("Expected older block 5 value length to be 5, got %d", len(val5Older))
	}

	rel5Older, err := deserializeOlderValueScan(val5Older)
	if err != nil {
		t.Fatalf("Failed to deserialize older value: %v", err)
	}
	if !equalUint16Slices(rel5Older, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5Older)
	}

	// Verify block 10 is latest (0x01)
	keyBlock10 := make([]byte, 41)
	copy(keyBlock10, prefix)
	binary.BigEndian.PutUint64(keyBlock10[33:], 10)

	val10 := getRaw(keyBlock10)
	if len(val10) < 9 {
		t.Fatalf("Value too short: %d", len(val10))
	}
	if val10[0] != flagLatest {
		t.Errorf("Expected block 10 to be latest (0x01), got %x", val10[0])
	}
	cum10 := binary.BigEndian.Uint64(val10[1:9])
	if cum10 != 3 {
		t.Errorf("Expected block 10 cumulative count to be 3, got %d", cum10)
	}

	_, range10, rel10, err := deserializeLatestValueScan(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for block 10: %v", err)
	}
	if range10.End() != 3 {
		t.Errorf("Expected range end to be 3, got %d", range10.End())
	}
	if !equalUint16Slices(rel10, []uint16{0}) {
		t.Errorf("Expected rel indices [0], got %v", rel10)
	}
}

func TestNewChunkScanStoreInvalidChunkSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-chunk-scan-invalid-size-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := NewChunkScanStore(tmpDir, 0); err == nil {
		t.Error("Expected error for chunkSize = 0")
	}

	if _, err := NewChunkScanStore(tmpDir, 65537); err == nil {
		t.Error("Expected error for chunkSize = 65537")
	}

	// Boundary check
	s1, err := NewChunkScanStore(tmpDir, 1)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 1: %v", err)
	} else {
		s1.Close()
	}

	s2, err := NewChunkScanStore(tmpDir, 65536)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 65536: %v", err)
	} else {
		s2.Close()
	}
}
