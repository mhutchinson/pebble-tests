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

func TestSealingChunkStoreCorrectness_Size2(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-test-2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create SealingChunkStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
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

func TestSealingChunkStoreCorrectness_Size1024(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-test-1024-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkStore(tmpDir, 1024)
	if err != nil {
		t.Fatalf("Failed to create SealingChunkStore: %v", err)
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

func TestSealingChunkStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create ChunkStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify chunk 5 is latest
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], 5)

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

	val5 := getRaw(keyChunk5)
	p5PrevChunkNum, p5CumulativeCount, range5, _, err := deserializeLatestValueSealing(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for chunk 5: %v", err)
	}
	if p5PrevChunkNum != 0 {
		t.Errorf("Expected chunk 5 prev to be 0, got %d", p5PrevChunkNum)
	}
	if p5CumulativeCount != 2 {
		t.Errorf("Expected chunk 5 cumulative count to be 2, got %d", p5CumulativeCount)
	}
	if range5.End() != 0 {
		t.Errorf("Expected chunk 5 range end to be 0, got %d", range5.End())
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify chunk 5 is now older (0x02)
	val5Older := getRaw(keyChunk5)
	if len(val5Older) < 17 {
		t.Fatalf("Value too short: %d", len(val5Older))
	}
	if val5Older[0] != flagOlder {
		t.Errorf("Expected chunk 5 to be older (0x02), got %x", val5Older[0])
	}
	prev5Older := binary.BigEndian.Uint64(val5Older[1:9])
	if prev5Older != 0 {
		t.Errorf("Expected chunk 5 older prev to be 0, got %d", prev5Older)
	}
	cum5Older := binary.BigEndian.Uint64(val5Older[9:17])
	if cum5Older != 2 {
		t.Errorf("Expected chunk 5 older cumulative count to be 2, got %d", cum5Older)
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

	// Verify chunk 10 is latest (0x01)
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], 10)

	val10 := getRaw(keyChunk10)
	p10PrevChunkNum, p10CumulativeCount, range10, _, err := deserializeLatestValueSealing(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for chunk 10: %v", err)
	}
	if p10PrevChunkNum != 5 {
		t.Errorf("Expected chunk 10 prev to be 5, got %d", p10PrevChunkNum)
	}
	if p10CumulativeCount != 3 {
		t.Errorf("Expected chunk 10 cumulative count to be 3, got %d", p10CumulativeCount)
	}
	if range10.End() != 2 {
		t.Errorf("Expected chunk 10 range end to be 2, got %d", range10.End())
	}
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

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], 5)

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

	val5 := getRaw(keyChunk5)
	range5, rel5, err := deserializeChunkValue(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 5 value: %v", err)
	}
	if range5.End() != 0 {
		t.Errorf("Expected chunk 5 range end to be 0, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// In No-Seal, chunk 5 is NOT modified/rewritten when crossing boundary.
	// Verify its value is exactly identical.
	val5After := getRaw(keyChunk5)
	if !bytes.Equal(val5, val5After) {
		t.Errorf("Expected chunk 5 value to remain unchanged, got diff")
	}

	// Verify chunk 10
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], 10)

	val10 := getRaw(keyChunk10)
	range10, rel10, err := deserializeChunkValue(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 10 value: %v", err)
	}
	if range10.End() != 2 {
		t.Errorf("Expected chunk 10 range end to be 2 (covering chunk 5), got %d", range10.End())
	}
	if !equalUint16Slices(rel10, []uint16{0}) {
		t.Errorf("Expected rel indices [0], got %v", rel10)
	}
}

func TestNewSealingChunkStoreInvalidChunkSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-invalid-size-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := NewSealingChunkStore(tmpDir, 0); err == nil {
		t.Error("Expected error for chunkSize = 0")
	}

	if _, err := NewSealingChunkStore(tmpDir, 65537); err == nil {
		t.Error("Expected error for chunkSize = 65537")
	}

	// Boundary check
	s1, err := NewSealingChunkStore(tmpDir, 1)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 1: %v", err)
	} else {
		s1.Close()
	}

	s2, err := NewSealingChunkStore(tmpDir, 65536)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 65536: %v", err)
	} else {
		s2.Close()
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

func TestSealingChunkScanStoreCorrectness_Size2(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-scan-test-2-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create SealingChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestSealingChunkScanStoreCorrectness_Size1024(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-scan-test-1024-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkScanStore(tmpDir, 1024)
	if err != nil {
		t.Fatalf("Failed to create SealingChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestSealingChunkScanStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-scan-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewSealingChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create SealingChunkScanStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify chunk 5 is latest
	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], 5)

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

	val5 := getRaw(keyChunk5)
	if len(val5) < 9 {
		t.Fatalf("Value too short: %d", len(val5))
	}
	if val5[0] != flagLatest {
		t.Errorf("Expected chunk 5 to be latest (0x01), got %x", val5[0])
	}
	cum5 := binary.BigEndian.Uint64(val5[1:9])
	if cum5 != 2 {
		t.Errorf("Expected chunk 5 cumulative count to be 2, got %d", cum5)
	}

	// Range and rel indices check
	_, range5, rel5, err := deserializeLatestValueScanSealing(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for chunk 5: %v", err)
	}
	if range5.End() != 0 {
		t.Errorf("Expected range end to be 0, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// Verify chunk 5 is now older (0x02)
	val5Older := getRaw(keyChunk5)
	if len(val5Older) == 0 {
		t.Fatalf("Value is empty")
	}
	if val5Older[0] != flagOlder {
		t.Errorf("Expected chunk 5 to be older (0x02), got %x", val5Older[0])
	}
	if len(val5Older) != 5 {
		t.Errorf("Expected older chunk 5 value length to be 5, got %d", len(val5Older))
	}

	rel5Older, err := deserializeOlderValueScanSealing(val5Older)
	if err != nil {
		t.Fatalf("Failed to deserialize older value: %v", err)
	}
	if !equalUint16Slices(rel5Older, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5Older)
	}

	// Verify chunk 10 is latest (0x01)
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], 10)

	val10 := getRaw(keyChunk10)
	if len(val10) < 9 {
		t.Fatalf("Value too short: %d", len(val10))
	}
	if val10[0] != flagLatest {
		t.Errorf("Expected chunk 10 to be latest (0x01), got %x", val10[0])
	}
	cum10 := binary.BigEndian.Uint64(val10[1:9])
	if cum10 != 3 {
		t.Errorf("Expected chunk 10 cumulative count to be 3, got %d", cum10)
	}

	_, range10, rel10, err := deserializeLatestValueScanSealing(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize latest value for chunk 10: %v", err)
	}
	if range10.End() != 2 {
		t.Errorf("Expected range end to be 2, got %d", range10.End())
	}
	if !equalUint16Slices(rel10, []uint16{0}) {
		t.Errorf("Expected rel indices [0], got %v", rel10)
	}
}

func TestNewSealingChunkScanStoreInvalidChunkSize(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-sealing-chunk-scan-invalid-size-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	if _, err := NewSealingChunkScanStore(tmpDir, 0); err == nil {
		t.Error("Expected error for chunkSize = 0")
	}

	if _, err := NewSealingChunkScanStore(tmpDir, 65537); err == nil {
		t.Error("Expected error for chunkSize = 65537")
	}

	// Boundary check
	s1, err := NewSealingChunkScanStore(tmpDir, 1)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 1: %v", err)
	} else {
		s1.Close()
	}

	s2, err := NewSealingChunkScanStore(tmpDir, 65536)
	if err != nil {
		t.Errorf("Unexpected error for chunkSize = 65536: %v", err)
	} else {
		s2.Close()
	}
}

// --- No-Seal ChunkScanStore Tests ---

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

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], 5)

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

	val5 := getRaw(keyChunk5)
	range5, rel5, err := deserializeChunkValue(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 5 value: %v", err)
	}
	if range5.End() != 0 {
		t.Errorf("Expected chunk 5 range end to be 0, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// In No-Seal, chunk 5 is NOT modified/rewritten when crossing boundary.
	// Verify its value is exactly identical.
	val5After := getRaw(keyChunk5)
	if !bytes.Equal(val5, val5After) {
		t.Errorf("Expected chunk 5 value to remain unchanged, got diff")
	}

	// Verify chunk 10
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], 10)

	val10 := getRaw(keyChunk10)
	range10, rel10, err := deserializeChunkValue(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 10 value: %v", err)
	}
	if range10.End() != 2 {
		t.Errorf("Expected chunk 10 range end to be 2 (covering chunk 5), got %d", range10.End())
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

func TestCrossEngineConsistency(t *testing.T) {
	ctx := context.Background()
	key1 := sha256.Sum256([]byte("consistent-key-1"))
	key2 := sha256.Sum256([]byte("consistent-key-2"))

	// Create temp directories
	dirFlat, _ := os.MkdirTemp("", "pebble-consistency-flat-*")
	defer os.RemoveAll(dirFlat)
	dirLog, _ := os.MkdirTemp("", "pebble-consistency-log-*")
	defer os.RemoveAll(dirLog)
	dirSealingChunk, _ := os.MkdirTemp("", "pebble-consistency-sealing-chunk-*")
	defer os.RemoveAll(dirSealingChunk)
	dirSealingScan, _ := os.MkdirTemp("", "pebble-consistency-sealing-scan-*")
	defer os.RemoveAll(dirSealingScan)
	dirChunk, _ := os.MkdirTemp("", "pebble-consistency-chunk-*")
	defer os.RemoveAll(dirChunk)
	dirScan, _ := os.MkdirTemp("", "pebble-consistency-scan-*")
	defer os.RemoveAll(dirScan)
	dirPrefixScan, _ := os.MkdirTemp("", "pebble-consistency-prefix-scan-*")
	defer os.RemoveAll(dirPrefixScan)
	dirInvertedScan, _ := os.MkdirTemp("", "pebble-consistency-inverted-scan-*")
	defer os.RemoveAll(dirInvertedScan)

	sFlat, _ := NewFlatStore(dirFlat)
	defer sFlat.Close()
	sLog, _ := NewLogStore(dirLog)
	defer sLog.Close()
	sSealingChunk, _ := NewSealingChunkStore(dirSealingChunk, 4)
	defer sSealingChunk.Close()
	sSealingScan, _ := NewSealingChunkScanStore(dirSealingScan, 4)
	defer sSealingScan.Close()
	sChunk, _ := NewChunkStore(dirChunk, 4) // chunk size 4
	defer sChunk.Close()
	sScan, _ := NewChunkScanStore(dirScan, 4) // chunk size 4
	defer sScan.Close()
	sPrefixScan, _ := NewPrefixChunkScanStore(dirPrefixScan, 4)
	defer sPrefixScan.Close()
	sInvertedScan, _ := NewInvertedPrefixChunkScanStore(dirInvertedScan, 4)
	defer sInvertedScan.Close()

	stores := []IndexStore{sFlat, sLog, sSealingChunk, sSealingScan, sChunk, sScan, sPrefixScan, sInvertedScan}
	names := []string{"FlatStore", "LogStore", "SealingChunkStore", "SealingChunkScanStore", "ChunkStore", "ChunkScanStore", "PrefixChunkScanStore", "InvertedPrefixChunkScanStore"}

	// Sequence of writes
	batches := []map[[32]byte][]uint64{
		{
			key1: {1, 3, 5},
			key2: {2, 4},
		},
		{
			key1: {7, 8, 12, 13}, // seals a chunk on chunk size 4
			key2: {6, 9},
		},
		{
			key1: {14, 15, 20},
			key2: {10},
		},
	}

	for _, batch := range batches {
		for i, s := range stores {
			if err := s.WriteBatch(ctx, batch); err != nil {
				t.Fatalf("%s WriteBatch failed: %v", names[i], err)
			}
		}
	}

	// Verify Lookups are consistent
	for _, key := range [][32]byte{key1, key2} {
		for start := uint64(0); start <= 15; start++ {
			var got [][]uint64
			for i, s := range stores {
				res, err := s.Lookup(ctx, key, start)
				if err != nil {
					t.Fatalf("%s Lookup failed for start=%d: %v", names[i], start, err)
				}
				got = append(got, res)
			}
			// Compare all results to first
			for i := 1; i < len(stores); i++ {
				if !equalSlices(got[i], got[0]) {
					t.Errorf("Lookup inconsistency for key %x start %d: %s got %v, %s got %v",
						key[:4], start, names[i], got[i], names[0], got[0])
				}
			}
		}
	}

	// Verify GetSubRoots are consistent
	for _, key := range [][32]byte{key1, key2} {
		var got [][32]byte
		for i, s := range stores {
			res, err := s.GetSubRoot(ctx, key)
			if err != nil {
				t.Fatalf("%s GetSubRoot failed: %v", names[i], err)
			}
			got = append(got, res)
		}
		for i := 1; i < len(stores); i++ {
			if got[i] != got[0] {
				t.Errorf("GetSubRoot inconsistency for key %x: %s got %x, %s got %x",
					key[:4], names[i], got[i], names[0], got[0])
			}
		}
	}
}

func TestSortedKeys(t *testing.T) {
	// Test empty map
	if keys := sortedKeys(nil); len(keys) != 0 {
		t.Errorf("Expected 0 keys, got %d", len(keys))
	}
	if keys := sortedKeys(map[[32]byte][]uint64{}); len(keys) != 0 {
		t.Errorf("Expected 0 keys, got %d", len(keys))
	}

	// Test populated map
	k1 := [32]byte{0x50}
	k2 := [32]byte{0x10}
	k3 := [32]byte{0xff}
	k4 := [32]byte{0x00}
	k5 := [32]byte{0x80}

	updates := map[[32]byte][]uint64{
		k1: {1},
		k2: {2},
		k3: {3},
		k4: {4},
		k5: {5},
	}

	keys := sortedKeys(updates)
	if len(keys) != 5 {
		t.Fatalf("Expected 5 keys, got %d", len(keys))
	}

	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i-1][:], keys[i][:]) >= 0 {
			t.Errorf("Keys not strictly sorted: keys[%d]=%x >= keys[%d]=%x", i-1, keys[i-1], i, keys[i])
		}
	}

	if keys[0] != k4 || keys[1] != k2 || keys[2] != k1 || keys[3] != k5 || keys[4] != k3 {
		t.Errorf("Unexpected sorted keys order: %v", keys)
	}
}

func TestWriteBatch_UnsortedKeysOrder(t *testing.T) {
	ctx := context.Background()

	dirChunk, _ := os.MkdirTemp("", "pebble-unsorted-chunk-*")
	defer os.RemoveAll(dirChunk)
	dirScan, _ := os.MkdirTemp("", "pebble-unsorted-scan-*")
	defer os.RemoveAll(dirScan)
	dirSealingChunk, _ := os.MkdirTemp("", "pebble-unsorted-sealing-chunk-*")
	defer os.RemoveAll(dirSealingChunk)
	dirSealingScan, _ := os.MkdirTemp("", "pebble-unsorted-sealing-scan-*")
	defer os.RemoveAll(dirSealingScan)
	dirLog, _ := os.MkdirTemp("", "pebble-unsorted-log-*")
	defer os.RemoveAll(dirLog)
	dirFlat, _ := os.MkdirTemp("", "pebble-unsorted-flat-*")
	defer os.RemoveAll(dirFlat)
	dirPrefixScan, _ := os.MkdirTemp("", "pebble-unsorted-prefix-scan-*")
	defer os.RemoveAll(dirPrefixScan)
	dirInvertedScan, _ := os.MkdirTemp("", "pebble-unsorted-inverted-scan-*")
	defer os.RemoveAll(dirInvertedScan)

	sChunk, _ := NewChunkStore(dirChunk, 4)
	defer sChunk.Close()
	sScan, _ := NewChunkScanStore(dirScan, 4)
	defer sScan.Close()
	sSealingChunk, _ := NewSealingChunkStore(dirSealingChunk, 4)
	defer sSealingChunk.Close()
	sSealingScan, _ := NewSealingChunkScanStore(dirSealingScan, 4)
	defer sSealingScan.Close()
	sLog, _ := NewLogStore(dirLog)
	defer sLog.Close()
	sFlat, _ := NewFlatStore(dirFlat)
	defer sFlat.Close()
	sPrefixScan, _ := NewPrefixChunkScanStore(dirPrefixScan, 4)
	defer sPrefixScan.Close()
	sInvertedScan, _ := NewInvertedPrefixChunkScanStore(dirInvertedScan, 4)
	defer sInvertedScan.Close()

	stores := []IndexStore{sFlat, sLog, sSealingChunk, sSealingScan, sChunk, sScan, sPrefixScan, sInvertedScan}

	// 1. Test empty batch
	for _, s := range stores {
		if err := s.WriteBatch(ctx, map[[32]byte][]uint64{}); err != nil {
			t.Fatalf("Empty WriteBatch failed: %v", err)
		}
	}

	// 2. Test batch with multiple keys
	kZ := [32]byte{0xfe}
	kA := [32]byte{0x01}
	kM := [32]byte{0x80}

	batch := map[[32]byte][]uint64{
		kZ: {10, 11, 20},
		kA: {1, 2, 3, 5},
		kM: {7, 8, 9},
	}

	for _, s := range stores {
		if err := s.WriteBatch(ctx, batch); err != nil {
			t.Fatalf("WriteBatch with unsorted map keys failed: %v", err)
		}
	}

	// Verify lookups
	for _, key := range [][32]byte{kA, kM, kZ} {
		var results [][]uint64
		for _, s := range stores {
			res, err := s.Lookup(ctx, key, 0)
			if err != nil {
				t.Fatalf("Lookup failed: %v", err)
			}
			results = append(results, res)
		}
		for i := 1; i < len(stores); i++ {
			if !equalSlices(results[i], results[0]) {
				t.Errorf("Mismatch in lookup results for key %x: store %d got %v, store 0 got %v", key[:2], i, results[i], results[0])
			}
		}
	}
}

func TestPrefixChunkScanStoreCorrectness_Size65536(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-prefix-chunk-scan-test-65536-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewPrefixChunkScanStore(tmpDir, 65536)
	if err != nil {
		t.Fatalf("Failed to create PrefixChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestPrefixChunkScanStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-prefix-chunk-scan-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewPrefixChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create PrefixChunkScanStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key"))

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], 5)

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

	val5 := getRaw(keyChunk5)
	range5, rel5, err := deserializeChunkValue(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 5 value: %v", err)
	}
	if range5.End() != 0 {
		t.Errorf("Expected chunk 5 range end to be 0, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// In No-Seal, chunk 5 is NOT modified/rewritten when crossing boundary.
	// Verify its value is exactly identical.
	val5After := getRaw(keyChunk5)
	if !bytes.Equal(val5, val5After) {
		t.Errorf("Expected chunk 5 value to remain unchanged, got diff")
	}

	// Verify chunk 10
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], 10)

	val10 := getRaw(keyChunk10)
	range10, rel10, err := deserializeChunkValue(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 10 value: %v", err)
	}
	if range10.End() != 2 {
		t.Errorf("Expected chunk 10 range end to be 2 (covering chunk 5), got %d", range10.End())
	}
	if !equalUint16Slices(rel10, []uint16{0}) {
		t.Errorf("Expected rel indices [0], got %v", rel10)
	}
}

func TestInvertedPrefixChunkScanStoreCorrectness_Size65536(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-inverted-prefix-test-65536-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewInvertedPrefixChunkScanStore(tmpDir, 65536)
	if err != nil {
		t.Fatalf("Failed to create InvertedPrefixChunkScanStore: %v", err)
	}
	defer s.Close()

	runIndexStoreTests(t, s)
}

func TestInvertedPrefixChunkScanStoreRawInspection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-inverted-prefix-inspect-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := NewInvertedPrefixChunkScanStore(tmpDir, 2)
	if err != nil {
		t.Fatalf("Failed to create InvertedPrefixChunkScanStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := sha256.Sum256([]byte("inspect-key-inverted"))

	// Write first batch: index 10, 11 (both chunk 5)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {10, 11}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	prefix := make([]byte, 33)
	prefix[0] = chunkPrefix
	copy(prefix[1:], key[:])

	keyChunk5 := make([]byte, 41)
	copy(keyChunk5, prefix)
	binary.BigEndian.PutUint64(keyChunk5[33:], invertChunkNum(5))

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

	val5 := getRaw(keyChunk5)
	range5, rel5, err := deserializeChunkValue(val5)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 5 value: %v", err)
	}
	if range5.End() != 0 {
		t.Errorf("Expected chunk 5 range end to be 0, got %d", range5.End())
	}
	if !equalUint16Slices(rel5, []uint16{0, 1}) {
		t.Errorf("Expected rel indices [0, 1], got %v", rel5)
	}

	// Write second batch: index 20 (chunk 10)
	if err := s.WriteBatch(ctx, map[[32]byte][]uint64{key: {20}}); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}

	// In No-Seal, chunk 5 is NOT modified/rewritten when crossing boundary.
	val5After := getRaw(keyChunk5)
	if !bytes.Equal(val5, val5After) {
		t.Errorf("Expected chunk 5 value to remain unchanged, got diff")
	}

	// Verify chunk 10
	keyChunk10 := make([]byte, 41)
	copy(keyChunk10, prefix)
	binary.BigEndian.PutUint64(keyChunk10[33:], invertChunkNum(10))

	val10 := getRaw(keyChunk10)
	range10, rel10, err := deserializeChunkValue(val10)
	if err != nil {
		t.Fatalf("Failed to deserialize chunk 10 value: %v", err)
	}
	if range10.End() != 2 {
		t.Errorf("Expected chunk 10 range end to be 2 (covering chunk 5), got %d", range10.End())
	}
	if !equalUint16Slices(rel10, []uint16{0}) {
		t.Errorf("Expected rel indices [0], got %v", rel10)
	}
}
