# Pebble VIndex Storage Layout Benchmarks

This repository contains benchmarks evaluating different storage layouts for the [Verifiable Index (VIndex)](https://github.com/transparency-dev/incubator/tree/main/vindex).

## Implementation Outlines

We evaluated three different storage layouts for storing the index data in Pebble DB:

1. **PebbleFlat (Flat List)**
   - Maps `Hash(Key)` to a serialized list of all its input log indices (`[]uint64`).
   - **Pros**: Fast read lookups via a single `Get`.
   - **Cons**: High write amplification for hot keys (requires rewriting the entire history of a key on every append).

2. **PebbleLog (Log Construction)**
   - Maps `Hash(Key) + [Input-Log-Index]` to a serialized `compact.Range` (mini Merkle tree) state. Old values are cleared.
   - **Pros**: Small write size and low write amplification.
   - **Cons**: Read lookups require a prefix scan. The write path requires an iterator seek to find the latest index, which can suffer from severe stalls during background compactions.

3. **PebbleChunk (Chunked Log)**
   - Groups indices into blocks. Maps `Hash(Key) + [BlockNum]` to a chunk of relative `uint16` indices. Blocks are backward-linked.
   - **Pros**: Caps write size, eliminates prefix scans by using point gets following links, and reduces key cardinality, resolving write stalls.

## 1 Million Entries (Stage 1)

The benchmarks were run with 1,000,000 entries and a batch size of 1k (1k batches) across three different workloads:

- **Mode A**: Low key cardinality (10 keys) — represents highly active "hot keys".
- **Mode B**: High key cardinality (1,000,000 keys) — represents highly distributed keys.
- **Mode C**: Medium key cardinality (100,000 keys) — represents a mixed workload.

### Mode A (10 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ----------- | ------------ | ---------------------- | --------------------- |
| flat               | 23801.89             | 43.907119ms | 82.80991ms  | 100.875655ms | 43.17 MB               | 4.36 MB               |
| log                | 140605.93            | 6.23888ms   | 15.498355ms | 21.817667ms  | 13.75 MB               | 5.06 MB               |
| chunk (size=256)   | 144950.36            | 5.953903ms  | 13.256819ms | 23.561318ms  | 7.78 MB                | 2.35 MB               |
| chunk (size=1024)  | 144425.82            | 6.063549ms  | 13.405561ms | 17.539499ms  | 6.05 MB                | 2.25 MB               |
| chunk (size=65536) | 145242.98            | 6.044846ms  | 16.164339ms | 38.440189ms  | 17.85 MB               | 1.93 MB               |

### Mode B (1,000,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 20098.20             | 50.606851ms | 102.729605ms | 137.632958ms | 83.79 MB               | 74.02 MB              |
| log                | 18208.71             | 53.369798ms | 128.582198ms | 176.31029ms  | 84.16 MB               | 73.80 MB              |
| chunk (size=256)   | 18195.30             | 55.793147ms | 99.607997ms  | 118.900432ms | 88.27 MB               | 79.31 MB              |
| chunk (size=1024)  | 16808.38             | 59.135134ms | 123.05721ms  | 178.955223ms | 88.13 MB               | 79.19 MB              |
| chunk (size=65536) | 18540.39             | 54.812044ms | 92.933905ms  | 137.80529ms  | 84.72 MB               | 75.69 MB              |

### Mode C (100,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 13813.71             | 77.498398ms | 141.215111ms | 182.707195ms | 33.77 MB               | 10.38 MB              |
| log                | 30277.71             | 31.433573ms | 60.126492ms  | 69.969835ms  | 23.19 MB               | 11.91 MB              |
| chunk (size=256)   | 31605.17             | 31.209186ms | 59.288879ms  | 74.381736ms  | 26.67 MB               | 15.28 MB              |
| chunk (size=1024)  | 28843.70             | 32.447286ms | 87.643634ms  | 122.131601ms | 26.27 MB               | 13.76 MB              |
| chunk (size=65536) | 32488.12             | 31.038606ms | 53.389104ms  | 64.508866ms  | 22.89 MB               | 9.34 MB               |

## 20 Million Entries (Stage 2)

The benchmarks were run with 20,000,000 entries and a batch size of 1k (20k batches) across three different workloads:

- **Mode A**: Low key cardinality (10 keys) — represents highly active "hot keys".
- **Mode B**: High key cardinality (1,000,000 keys) — represents highly distributed keys.
- **Mode C**: Medium key cardinality (100,000 keys) — represents a mixed workload.

The `flat` engine was dropped as it was already far too slow to progress to the next round.

### Mode A (10 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ----------- | ------------ | ---------------------- | --------------------- |
| log                | 101326.53            | 6.662839ms  | 18.028345ms | 5.654574149s | 121.81 MB              | 101.11 MB             |
| chunk (size=256)   | 209532.72            | 3.99436ms   | 8.021695ms  | 15.532261ms  | 60.06 MB               | 46.90 MB              |
| chunk (size=1024)  | 209426.89            | 3.852479ms  | 9.534082ms  | 22.869191ms  | 59.36 MB               | 44.84 MB              |
| chunk (size=65536) | 157051.75            | 5.588079ms  | 13.600742ms | 39.419239ms  | 54.62 MB               | 38.64 MB              |

### Mode B (1,000,000 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency  | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ------------ | ------------ | ------------ | ---------------------- | --------------------- |
| log                | 10098.67             | 95.320506ms  | 179.32436ms  | 342.796618ms | 339.35 MB              | 246.99 MB             |
| chunk (size=256)   | 8825.88              | 106.177618ms | 263.327886ms | 875.552698ms | 511.83 MB              | 431.28 MB             |
| chunk (size=1024)  | 9223.16              | 104.508515ms | 215.546115ms | 440.865051ms | 516.21 MB              | 436.96 MB             |
| chunk (size=65536) | 9255.65              | 104.744988ms | 200.535653ms | 364.835013ms | 482.70 MB              | 403.12 MB             |

### Mode C (100,000 keys, 20,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| log                | 17003.73             | 58.086233ms | 105.692343ms | 204.478501ms | 181.36 MB              | 142.30 MB             |
| chunk (size=256)   | 18007.66             | 53.937375ms | 112.095909ms | 221.037373ms | 251.01 MB              | 216.45 MB             |
| chunk (size=1024)  | 18906.96             | 51.472415ms | 101.885721ms | 222.893557ms | 219.54 MB              | 184.47 MB             |
| chunk (size=65536) | 21221.15             | 45.407513ms | 87.180437ms  | 186.326432ms | 145.46 MB              | 108.02 MB             |
