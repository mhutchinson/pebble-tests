# Pebble VIndex Storage Layout Benchmarks & Recommended Design

Based on extensive benchmarking, we recommend **`PebbleChunkScan` (Chunked Log with Range Scan)** as the default storage layout for the VIndex. It provides the best balance of write throughput, read latency, and storage efficiency for mixed workloads.

## Terminology

*   **Chunk / Chunk**: Synonymous terms referring to the logical partition of the index log for a specific key.
*   **Chunk Size / Chunk Size**: The logical capacity of a chunk, defined as the number of sequence numbers (logical indices) it covers (e.g., 65536). It does **not** refer to byte size or disk space limits.
*   **Index / Sequence Number**: The monotonically increasing logical log sequence number (e.g., 10, 11, 20) associated with a key.
*   **Active (Unsealed) Chunk**: The latest chunk for a key, which is still open to receive writes. Its cached compact range in the database is only updated to cover older, sealed chunks.
*   **Sealed (Older) Chunk**: A chunk that has been closed because the log index has progressed to a new chunk boundary. Once sealed, its relative indices are finalized, compact range metadata is stripped to save space, and it becomes read-only.

## Recommended Design: PebbleChunkScan

### Schema Organization

Data is partitioned into chunks (chunks) per key, where the chunk size (e.g., 65536) dictates the number of logical indices (sequence numbers) mapped to each chunk (i.e., chunkNum = index / chunkSize), not the byte size.

*   **Keys**: `[Prefix 'c' (1B)] + [Hash(Key) (32B)] + [ChunkNum (8B, BigEndian)]`
    *   Using BigEndian for `ChunkNum` ensures that chunks for a given key are stored sequentially on disk, enabling efficient range scans.
*   **Values**:
    *   **Latest Chunk (active)**: `[flagLatest (1B)] [cumulativeCount (8B)] [rangeLen (varint)] [serialized compact.Range] [relativeIndices ([]uint16)]`
        *   Contains the Merkle compact range state for incremental verification. Crucially, to optimize write performance, the serialized range in the `Latest` chunk only covers *sealed* chunks. The unsealed active chunk's elements (stored in `relativeIndices`) are hashed and appended to the range on-the-fly in memory during `GetSubRoot` queries.
    *   **Older Chunks (sealed)**: `[flagOlder (1B)] [relativeIndices ([]uint16)]`
        *   Merkle compact range metadata is stripped to save space. Only relative indices are stored.

### Key Operations

*   **Write (Append)**:
    1.  Perform a `SeekLT` using `Prefix + Hash(Key) + 0xFFFFFFFFFFFFFFFF` to find the current latest chunk.
    2.  Deserialize the latest chunk.
    3.  Append new indices. A chunk is sealed when a new index crosses the logical chunk boundary (i.e. `index / chunkSize != currBlockNum`), regardless of how many elements have actually been written to the current chunk. For sparse index sequences, a chunk can be sealed containing as few as one element.
        *   To seal: write the current chunk as `Older` (stripping the compact range metadata and writing only relative indices).
        *   Start a new chunk for the index crossing the boundary, writing it as the `Latest` chunk.
    4.  Commit the batch.
*   **Read (Lookup)**:
    1.  Calculate the starting chunk number: `startChunk = start / chunkSize`.
    2.  Initialize an iterator with an upper bound restricted to the key's prefix.
    3.  Seek to `Prefix + Hash(Key) + startChunk` (`SeekGE`).
    4.  Scan forward (`Next`) until the end of the key's prefix.
    5.  Reconstruct absolute indices from the relative offsets in each chunk.

### Performance Caveats
*   **Pure Write Latency on Hot Keys**: If the workload is exclusively writes to a very small set of keys (e.g., Mode A), the standard `PebbleChunk` (which uses point lookups and backward links) offers ~30% higher write throughput because it avoids range-scan overheads during compaction/seeks.
*   **Chunk Size Trade-off**: 
    *   Larger chunk sizes (e.g., 65536) optimize read performance (fewer chunks to scan) but degrade write throughput for hot keys due to the overhead of serializing larger chunks on every write.
    *   A chunk size of **1024** or **65536** is recommended depending on whether writes or reads are the primary bottleneck.

---

## Alternative Designs Evaluated

1.  **PebbleFlat (Flat List)**
    *   Maps `Hash(Key)` to a single serialized list of all its input log indices (`[]uint64`).
    *   **Pros**: Extremely fast reads (single `Get`).
    *   **Cons**: Unacceptable write amplification for hot keys (rewrites entire history on every append). Dropped from large-scale testing.

2.  **PebbleLog (Log Construction)**
    *   Maps `Hash(Key) + [Input-Log-Index]` to a serialized `compact.Range` state. Old values are cleared.
    *   **Pros**: Low write amplification.
    *   **Cons**: Reads require a prefix scan. Writes require an iterator seek to find the latest index, causing severe write stalls during compactions.

3.  **PebbleChunk (Chunked Log with Point Lookups)**
    *   Similar to `PebbleChunkScan` but chunks are backward-linked (`Older` chunks contain `prevBlockNum`).
    *   **Pros**: High write throughput.
    *   **Cons**: Reads require traversing the chain backwards via multiple random `db.Get` calls, resulting in terrible read performance for small chunk sizes.

---

## Detailed Benchmark Results

### 1 Million Entries (Stage 1)

The benchmarks were run with 1,000,000 entries and a batch size of 1k (1k batches) across three different workloads:

*   **Mode A**: Low key cardinality (10 keys) — represents highly active "hot keys".
*   **Mode B**: High key cardinality (1,000,000 keys) — represents highly distributed keys.
*   **Mode C**: Medium key cardinality (100,000 keys) — represents a mixed workload.

#### Mode A (10 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ----------- | ------------ | ---------------------- | --------------------- |
| flat               | 23801.89             | 43.907119ms | 82.80991ms  | 100.875655ms | 43.17 MB               | 4.36 MB               |
| log                | 140605.93            | 6.23888ms   | 15.498355ms | 21.817667ms  | 13.75 MB               | 5.06 MB               |
| chunk (size=256)   | 144950.36            | 5.953903ms  | 13.256819ms | 23.561318ms  | 7.78 MB                | 2.35 MB               |
| chunk (size=1024)  | 144425.82            | 6.063549ms  | 13.405561ms | 17.539499ms  | 6.05 MB                | 2.25 MB               |
| chunk (size=65536) | 145242.98            | 6.044846ms  | 16.164339ms | 38.440189ms  | 17.85 MB               | 1.93 MB               |
| chunk_scan (size=256) | 164319.60            | 5.344936ms  | 10.76363ms  | 16.217023ms  | 7.01 MB                | 2.03 MB               |
| chunk_scan (size=1024) | 180844.77           | 4.80907ms   | 10.245674ms | 11.964461ms  | 5.79 MB                | 2.10 MB               |
| chunk_scan (size=65536) | 166776.08          | 5.392836ms  | 12.873604ms | 18.855883ms  | 17.86 MB               | 1.93 MB               |

#### Mode B (1,000,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 20098.20             | 50.606851ms | 102.729605ms | 137.632958ms | 83.79 MB               | 74.02 MB              |
| log                | 18208.71             | 53.369798ms | 128.582198ms | 176.31029ms  | 84.16 MB               | 73.80 MB              |
| chunk (size=256)   | 18195.30             | 55.793147ms | 99.607997ms  | 118.900432ms | 88.27 MB               | 79.31 MB              |
| chunk (size=1024)  | 16808.38             | 59.135134ms | 123.05721ms  | 178.955223ms | 88.13 MB               | 79.19 MB              |
| chunk (size=65536) | 18540.39             | 54.812044ms | 92.933905ms  | 137.80529ms  | 84.72 MB               | 75.69 MB              |
| chunk_scan (size=256) | 18414.85             | 54.122398ms | 109.969391ms | 155.647732ms | 88.02 MB               | 78.34 MB              |
| chunk_scan (size=1024) | 19205.18            | 53.654313ms | 96.522618ms  | 101.025427ms | 87.73 MB               | 78.08 MB              |
| chunk_scan (size=65536) | 19800.68            | 52.941193ms | 87.806738ms  | 117.189269ms | 84.39 MB               | 74.62 MB              |

#### Mode C (100,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 13813.71             | 77.498398ms | 141.215111ms | 182.707195ms | 33.77 MB               | 10.38 MB              |
| log                | 30277.71             | 31.433573ms | 60.126492ms  | 69.969835ms  | 23.19 MB               | 11.91 MB              |
| chunk (size=256)   | 31605.17             | 31.209186ms | 59.288879ms  | 74.381736ms  | 26.67 MB               | 15.28 MB              |
| chunk (size=1024)  | 28843.70             | 32.447286ms | 87.643634ms  | 122.131601ms | 26.27 MB               | 13.76 MB              |
| chunk (size=65536) | 32488.12             | 31.038606ms | 53.389104ms  | 64.508866ms  | 22.89 MB               | 9.34 MB               |
| chunk_scan (size=256) | 32975.53             | 29.183117ms | 69.177475ms  | 102.557938ms | 23.29 MB               | 11.26 MB              |
| chunk_scan (size=1024) | 34241.01            | 28.774515ms | 53.392625ms  | 66.995118ms  | 23.35 MB               | 10.84 MB              |
| chunk_scan (size=65536) | 27117.87            | 32.723638ms | 77.731558ms  | 94.454527ms  | 22.13 MB               | 8.67 MB               |

---

### 20 Million Entries (Stage 2)

The benchmarks were run with 20,000,000 entries and a batch size of 1k (20k batches). The `flat` engine was dropped as it was already too slow.

#### Mode A (10 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ----------- | ------------ | ---------------------- | --------------------- |
| log                | 101326.53            | 6.662839ms  | 18.028345ms | 5.654574149s | 121.81 MB              | 101.11 MB             |
| chunk (size=256)   | 209532.72            | 3.99436ms   | 8.021695ms  | 15.532261ms  | 60.06 MB               | 46.90 MB              |
| chunk (size=1024)  | 209426.89            | 3.852479ms  | 9.534082ms  | 22.869191ms  | 59.36 MB               | 44.84 MB              |
| chunk (size=65536) | 157051.75            | 5.588079ms  | 13.600742ms | 39.419239ms  | 54.62 MB               | 38.64 MB              |
| chunk_scan (size=256) | 146907.79            | 5.78879ms   | 11.765758ms | 21.661716ms  | 53.01 MB               | 40.54 MB              |
| chunk_scan (size=1024) | 147275.83           | 5.728151ms  | 12.248778ms | 31.185962ms  | 56.65 MB               | 42.01 MB              |
| chunk_scan (size=65536) | 126103.43          | 7.013975ms  | 16.077945ms | 113.881585ms | 54.45 MB               | 38.59 MB              |

#### Mode B (1,000,000 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency  | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ------------ | ------------ | ------------ | ---------------------- | --------------------- |
| log                | 10098.67             | 95.320506ms  | 179.32436ms  | 342.796618ms | 339.35 MB              | 246.99 MB             |
| chunk (size=256)   | 8825.88              | 106.177618ms | 263.327886ms | 875.552698ms | 511.83 MB              | 431.28 MB             |
| chunk (size=1024)  | 9223.16              | 104.508515ms | 215.546115ms | 440.865051ms | 516.21 MB              | 436.96 MB             |
| chunk (size=65536) | 9255.65              | 104.744988ms | 200.535653ms | 364.835013ms | 482.70 MB              | 403.12 MB             |
| chunk_scan (size=256) | 10164.86             | 95.532301ms  | 157.643677ms | 350.406214ms | 360.73 MB              | 273.71 MB             |
| chunk_scan (size=1024) | 10069.89            | 96.483039ms  | 164.111434ms | 691.784474ms | 379.32 MB              | 289.24 MB             |
| chunk_scan (size=65536) | 10327.20            | 94.651833ms  | 153.404444ms | 287.951266ms | 364.35 MB              | 277.66 MB             |

#### Mode C (100,000 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| log                | 17003.73             | 58.086233ms | 105.692343ms | 204.478501ms | 181.36 MB              | 142.30 MB             |
| chunk (size=256)   | 18007.66             | 53.937375ms | 112.095909ms | 221.037373ms | 251.01 MB              | 216.45 MB             |
| chunk (size=1024)  | 18906.96             | 51.472415ms | 101.885721ms | 222.893557ms | 219.54 MB              | 184.47 MB             |
| chunk (size=65536) | 21221.15             | 45.407513ms | 87.180437ms  | 186.326432ms | 145.46 MB              | 108.02 MB             |
| chunk_scan (size=256) | 19541.34             | 50.225116ms | 87.957549ms  | 689.27636ms  | 164.61 MB              | 125.81 MB             |
| chunk_scan (size=1024) | 19280.51            | 46.613986ms | 113.14259ms  | 154.975179ms | 157.35 MB              | 118.74 MB             |
| chunk_scan (size=65536) | 22833.25            | 41.814453ms | 81.27592ms   | 259.341906ms | 111.99 MB              | 81.63 MB              |

---

### 1 Million Entries with Concurrent Readers (Stage 1 - Mixed Workload)

The benchmarks were run with 1,000,000 entries, Mode C (100,000 keys), and 5 concurrent readers with unlimited QPS:

| Engine             | Write QPS | Write p50    | Write p99    | Write Max    | Read QPS | Read p50    | Read p99     | Read Max     | Read Errors | Size Before | Size After |
| ------------------ | --------- | ------------ | ------------ | ------------ | -------- | ----------- | ------------ | ------------ | ----------- | ----------- | ---------- |
| flat               | 7917.50   | 139.39454ms  | 274.345592ms | 338.826115ms | 13314.02 | 113.104µs   | 3.581429ms   | 36.9392ms    | 0           | 33.78 MB    | 10.39 MB   |
| log                | 17673.99  | 59.275158ms  | 95.201944ms  | 134.366821ms | 624.37   | 661.01µs    | 60.303549ms  | 95.912546ms  | 0           | 23.16 MB    | 11.91 MB   |
| chunk (size=256)   | 15358.78  | 61.626421ms  | 183.757704ms | 214.128308ms | 46.23    | 72.630547ms | 433.25926ms  | 759.002514ms | 0           | 26.67 MB    | 15.28 MB   |
| chunk (size=1024)  | 15199.55  | 66.369335ms  | 135.004613ms | 156.121054ms | 163.46   | 21.825299ms | 123.129903ms | 196.45796ms  | 0           | 26.26 MB    | 13.76 MB   |
| chunk (size=65536) | 8543.92   | 121.327054ms | 349.008702ms | 496.87062ms  | 2285.58  | 1.680145ms  | 8.107331ms   | 19.893486ms  | 0           | 22.89 MB    | 9.34 MB    |
| chunk_scan (size=256) | 9398.27   | 107.934227ms | 207.977298ms | 227.252481ms | 1926.57  | 1.152185ms  | 14.113919ms  | 35.841485ms  | 0           | 22.49 MB    | 11.26 MB   |
| chunk_scan (size=1024) | 9071.41  | 107.621235ms | 218.903765ms | 261.849767ms | 3100.65  | 730.268µs   | 9.196735ms   | 34.688999ms  | 0           | 23.35 MB    | 10.85 MB   |
| chunk_scan (size=65536) | 9523.14  | 104.660421ms | 215.834539ms | 304.066094ms | 5156.95  | 297.706µs   | 5.647521ms   | 17.558456ms  | 0           | 22.09 MB    | 8.68 MB    |

Introducing 5 concurrent readers roughly halves the write throughput across all engines compared to the write-only Mode C benchmark (e.g., `log` falls from approx. 30k to approx. 17.6k write QPS). 

The `flat` engine continues to provide the lowest read latency (approx. 113µs p50) but suffers from high write latency. Among the chunked engines, `chunk (size=65536)` offers much better read performance (2,285 QPS, 1.68ms p50) than smaller chunk sizes which suffer from traversing long chains of backward-linked chunks. However, `chunk (size=65536)`'s write throughput (approx. 8.5k QPS) is significantly lower than smaller chunk sizes because the larger chunk size allows hot key chunks to grow large (up to 128KB), increasing serialization and write overhead.
