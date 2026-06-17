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

## Benchmark Results

The benchmarks were run with 1,000,000 entries and a batch size of 1,000 (1000 batches) across three different workloads:

- **Mode A**: Low key cardinality (10 keys) — represents highly active "hot keys".
- **Mode B**: High key cardinality (1,000,000 keys) — represents highly distributed keys.
- **Mode C**: Medium key cardinality (100,000 keys) — represents a mixed workload.

### Mode A (10 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency | Max Latency | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ----------- | ----------- | ---------------------- | --------------------- |
| flat               | 23479.15             | 46.416769ms | 81.722954ms | 98.919039ms | 47.73 MB               | 43.59 MB              |
| log                | 144307.71            | 5.869852ms  | 17.76228ms  | 22.656661ms | 17.37 MB               | 18.96 MB              |
| chunk (size=256)   | 131567.30            | 6.653158ms  | 15.142042ms | 23.54206ms  | 7.79 MB                | 8.61 MB               |
| chunk (size=1024)  | 146699.65            | 5.967495ms  | 12.306849ms | 17.28786ms  | 6.05 MB                | 9.38 MB               |
| chunk (size=65536) | 144375.89            | 6.046565ms  | 16.002211ms | 22.742024ms | 17.85 MB               | 17.99 MB              |

### Mode B (1,000,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 18701.65             | 51.385579ms | 114.083554ms | 127.693089ms | 83.77 MB               | 159.50 MB             |
| log                | 19371.54             | 51.885228ms | 95.915163ms  | 125.153387ms | 84.29 MB               | 155.45 MB             |
| chunk (size=256)   | 17249.06             | 56.720257ms | 118.111988ms | 133.726757ms | 88.27 MB               | 165.68 MB             |
| chunk (size=1024)  | 17175.34             | 57.756996ms | 112.187935ms | 172.098277ms | 88.13 MB               | 169.44 MB             |
| chunk (size=65536) | 17742.31             | 56.031204ms | 121.683842ms | 179.453586ms | 84.72 MB               | 158.51 MB             |

### Mode C (100,000 keys, 1,000,000 entries)

| Engine             | Throughput (ops/sec) | p50 Latency | p99 Latency  | Max Latency  | Size Before Compaction | Size After Compaction |
| ------------------ | -------------------- | ----------- | ------------ | ------------ | ---------------------- | --------------------- |
| flat               | 12765.14             | 80.66848ms  | 154.720711ms | 196.422946ms | 38.03 MB               | 40.72 MB              |
| log                | 31611.72             | 30.469177ms | 60.702264ms  | 72.122794ms  | 23.19 MB               | 35.24 MB              |
| chunk (size=256)   | 29474.93             | 32.145106ms | 71.752669ms  | 86.305175ms  | 26.67 MB               | 38.95 MB              |
| chunk (size=1024)  | 29276.52             | 32.76887ms  | 67.662229ms  | 124.692856ms | 26.26 MB               | 36.93 MB              |
| chunk (size=65536) | 20032.92             | 43.810484ms | 92.934996ms  | 1.80769093s  | 27.37 MB               | 28.38 MB              |
