# Pebble VIndex Storage Layout Benchmarks & Recommended Design

Based on extensive benchmarking across multiple workloads (up to 20 million entries), we recommend **`PebbleInvertedPrefixChunkScan` (Inverted Chunk Numbers with Prefix Bloom Filters and Chunk Size 65536)** as the default, production-grade storage layout for the VIndex.

It achieves the highest write throughput, lowest latency, zero-I/O key existence checking via Bloom filters, and true O(1) active chunk location across both hot-key and high-cardinality workloads.

For detailed architectural specifications, failure modes, and schema design, see [DESIGN.md](DESIGN.md).

> **Note**: For historical benchmark comparisons across all prototype engines (`FlatStore`, `LogStore`, `Sealing` layouts, and smaller chunk sizes like 256/1024), see commit `b610d9bd2be55b3b7e4ad52a973d8343c71df9bf`.

---

## Terminology

*   **Chunk**: Logical partition of the index log for a specific key.
*   **Chunk Size**: The logical capacity of a chunk, defined as the number of logical indices (sequence numbers) it covers (default: **65536**). It does **not** refer to byte size or disk space limits.
*   **Index / Sequence Number**: The monotonically increasing logical log sequence number (e.g., 10, 11, 20) associated with a key.
*   **Active Chunk**: The highest chunk for a key, which is still open to receive writes.
*   **Sealed Chunk**: A chunk that has been logically closed because the index has progressed past its boundary. In the No-Seal layout, sealed chunks are never modified or rewritten.

---

## Recommended Design: `PebbleInvertedPrefixChunkScan`

### Schema Organization

Data is partitioned into chunks per key with logical chunk size 65536 (`chunkNum = index / chunkSize`).

*   **Keys**: `[Prefix 'c' (1B)] + [Hash(Key) (32B)] + [^chunkNum (8B, BigEndian)]`
    *   **Inverted Chunk Number (`^chunkNum`)**: Using `math.MaxUint64 - chunkNum` ensures that the latest active chunk (chunk N) has the **smallest key** among all chunks for that key prefix.
    *   **Custom Prefix Split**: A custom Pebble `Comparer` splits keys at byte 33 (`'c' + Hash(Key)`).
    *   **Prefix Bloom Filters**: Bloom filters (`bloom.FilterPolicy(10)`) are generated on the 33-byte prefix across all SSTable levels.
*   **Values**:
    *   **Uniform Value Schema**: `[serialized compact.Range] + [relativeIndices ([]uint16)]`
        *   There are **no prefix flags** (no distinction between active/sealed chunks on write).
        *   The serialized `compact.Range` in chunk N represents the finalized range state covering all elements in older chunks preceding chunk N (chunks 0 to N-1).
        *   The `relativeIndices` slice contains the logical offsets of elements written to this specific chunk N.
        *   **Delimitless Deserialization**: The range boundary is computed dynamically in memory as `8 + bits.OnesCount64(size) * 32` bytes without requiring a length prefix.

### Key Operations

*   **Write (Append)**:
    1.  **Lexicographical Key Sorting & Shared Iterator**:
        *   Sort the batch's keys in ascending byte order (`bytes.Compare`).
        *   Allocate a single `pebble.Iterator` before the loop and reuse it for all seeks in the batch.
    2.  **Instant O(1) Active Chunk Seek**:
        *   Call `iter.SeekPrefixGE(prefix)`.
        *   If the key does not exist, the Bloom filter skips all SSTables immediately (0 disk I/O).
        *   If the key exists, `SeekPrefixGE(prefix)` lands **directly on the latest active chunk** in O(1) because the latest chunk has the smallest inverted key.
    3.  **Deserialize & Append**:
        *   Deserialize `starting_range` and `relative_indices`.
        *   If a new index crosses the chunk boundary (`index / chunkSize != currChunkNum`):
            *   Write the modified chunk under `prefix + BigEndian(^currChunkNum)`. No sealing write or stripping is needed.
            *   Finalize the compact range in memory: append `relative_indices` to `starting_range`.
            *   Initialize a new chunk with `starting_range = finalized_range`.
        *   Append the offset `uint16(index % chunkSize)` to `relative_indices`.
    4.  **Commit Batch**: Serialize the active chunk, write it to Pebble under `prefix + BigEndian(^currChunkNum)`, and commit the batch atomically.

*   **Read (Lookup)**:
    1.  **Seek Lower Bound**: Calculate `startChunkNum = start / chunkSize` and `startKey = prefix + BigEndian(^startChunkNum)`.
    2.  **Scan Forward in Time**: Call `iter.SeekGE(startKey)` (adjusting with `iter.Prev()` if landing after `startKey`). Iterate backwards (`iter.Prev()`) while `bytes.HasPrefix(iter.Key(), prefix)` to traverse chunks from oldest (`startChunkNum`) to newest in chronological sequence.
    3.  **Reconstruct Indices**: Reconstruct absolute indices and slice starting from the requested `start` offset using the first chunk's `compact.Range.End()`.

*   **GetSubRoot**:
    1.  Call `iter.SeekPrefixGE(prefix)` to land directly on the latest chunk in O(1).
    2.  Append active relative indices to the compact range on-the-fly and return the computed Merkle root.

---

## Benchmark Results: Final Comparison (Chunk Size 65536)

All benchmarks below were executed with a batch size of 1,000 entries across three workloads:
*   **Mode A**: Low key cardinality (10 keys) — represents highly active "hot keys".
*   **Mode B**: High key cardinality (1,000,000 keys) — represents widely distributed, sparse keys.
*   **Mode C**: Medium key cardinality (100,000 keys) — represents mixed production workloads.

---

### Stage 1: 1 Million Entries (Write Only)

| Workload Mode | Engine | Write Throughput (QPS) | Write Latency (p50) | Write Latency (p99) | Size After Compaction |
| :--- | :--- | :---: | :---: | :---: | :---: |
| **Mode A** *(10 keys)* | `chunk_scan` | **190,912.49** | **2.93ms** | 103.78ms | 1.96 MB |
| | `prefix_chunk_scan` | 148,068.21 | 4.58ms | 109.14ms | 1.97 MB |
| | **`inverted_prefix_chunk_scan`** | 175,523.83 | 3.37ms | 107.45ms | 1.97 MB |
| | | | | | |
| **Mode B** *(1M keys)* | `chunk_scan` | 27,923.73 | 35.96ms | 105.03ms | 40.50 MB |
| | `prefix_chunk_scan` | 39,775.17 | 24.38ms | 48.52ms | 41.72 MB |
| | **`inverted_prefix_chunk_scan`** | **41,431.67** *(+48.4%)* | **23.73ms** | **45.97ms** | 41.94 MB |
| | | | | | |
| **Mode C** *(100k keys)* | `chunk_scan` | 40,144.43 | 21.83ms | 85.38ms | 11.93 MB |
| | `prefix_chunk_scan` | 39,190.96 | 23.20ms | 91.55ms | 12.02 MB |
| | **`inverted_prefix_chunk_scan`** | **64,446.37** *(+60.5%)* | **13.66ms** | **66.60ms** | 11.95 MB |

---

### Stage 1: 1 Million Entries with 5 Concurrent Readers (Mixed Workload)

| Engine | Write QPS | Write p50 | Write p99 | Read QPS | Read p50 | Read p99 | Size After |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| `chunk_scan` | 13,801.20 | 66.79ms | 228.54ms | 4,606.48 | 382.50µs | 6.34ms | 11.94 MB |
| `prefix_chunk_scan` | 12,985.36 | 74.85ms | 229.53ms | **4,864.41** | **354.88µs** | 6.05ms | 12.02 MB |
| **`inverted_prefix_chunk_scan`** | **19,049.79** *(+38.0%)* | **52.04ms** | **190.79ms** | 4,505.01 | 497.54µs | **5.52ms** | 11.95 MB |

---

### Stage 2: 20 Million Entries (Large Scale)

| Workload Mode | Engine | Write Throughput (QPS) | Write Latency (p50) | Write Latency (p99) | Size After Compaction |
| :--- | :--- | :---: | :---: | :---: | :---: |
| **Mode A** *(10 keys, ~31 chunks/key)* | `chunk_scan` | 200,119.84 | 2.66ms | 106.71ms | 39.59 MB |
| | `prefix_chunk_scan` | 27,262.23 | 37.09ms | 132.06ms | 39.69 MB |
| | **`inverted_prefix_chunk_scan`** | **206,238.05** | **2.54ms** | **104.63ms** | 39.72 MB |
| | | | | | |
| **Mode B** *(1M keys)* | `chunk_scan` | 14,487.68 | 65.88ms | 131.65ms | 963.08 MB |
| | `prefix_chunk_scan` | 13,131.93 | 74.78ms | 171.12ms | 965.81 MB |
| | **`inverted_prefix_chunk_scan`** | **15,803.92** *(+9.1%)* | **63.03ms** | **127.38ms** | 962.47 MB |
| | | | | | |
| **Mode C** *(100k keys, ~3 chunks/key)* | `chunk_scan` | **32,067.42** | **29.63ms** | **85.81ms** | 272.08 MB |
| | `prefix_chunk_scan` | 8,635.27 | 118.76ms | 216.84ms | 273.11 MB |
| | **`inverted_prefix_chunk_scan`** | 31,007.21 | 31.99ms | 86.51ms | 270.78 MB |

---

## Architectural Analysis & Trade-Off Summary

1. **The Flaw in `prefix_chunk_scan` (Standard Forward Chunk Numbers)**:
   - With normal chunk numbers, the latest chunk is at the end of the key prefix range (`chunkNum = N`).
   - Calling `SeekPrefixGE(prefix)` lands on `chunk 0`. To find the active chunk, it must scan forward through all older sealed chunks via `iter.Next()`.
   - As keys accumulate multiple chunks (e.g. Mode A at 20M entries with 31 chunks per key, or Mode C at 20M entries), this forward scan degrades write throughput by up to **7.5x**.

2. **The Triumph of `inverted_prefix_chunk_scan` (Inverted Chunk Numbers)**:
   - Inverting chunk numbers (`^chunkNum`) places the newest chunk at the very beginning of the prefix range.
   - Calling `SeekPrefixGE(prefix)` lands **directly on the active chunk in O(1)** with a single seek, completely eliminating forward scanning.
   - Combines the **Bloom filter acceleration** of prefix splits with the **instant active chunk seek** of reverse index order.
   - Achieves up to **+60.5% write throughput improvement** on 1M workloads and **206k QPS** on large-scale hot key workloads.
