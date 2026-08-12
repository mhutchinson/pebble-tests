package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mhutchinson/pebble-tests/harness"
	"github.com/mhutchinson/pebble-tests/store"
)

type benchmarkResult struct {
	engine     string
	results    *harness.Results
	err        error
	numReaders int
}

func main() {
	mode := flag.String("mode", "C", "Workload mode (A, B, C)")
	entries := flag.Int("entries", 100000, "Total number of log entries to simulate")
	batchSize := flag.Int("batch_size", 1000, "Number of entries per write batch")
	enginesStr := flag.String("engines", "flat,log,chunk,chunk_scan,sealing_chunk,sealing_chunk_scan", "Comma-separated list of engines to run (flat, log, chunk, chunk_scan, sealing_chunk, sealing_chunk_scan)")
	dbDir := flag.String("db_dir", "./tmp_db", "Root directory for temporary databases")
	chunkSizesStr := flag.String("chunk_sizes", "256,1024,65536", "Comma-separated list of chunk sizes to test for the chunk engine")
	readers := flag.Int("readers", 0, "Number of concurrent readers")
	readRate := flag.Int("read_rate", 0, "Target read QPS (0 for unlimited)")
	readDist := flag.String("read_dist", "zipfian", "Read distribution (uniform, zipfian, recent)")
	readStartPct := flag.Float64("read_start_percent", 20, "Percentage of writes completed before starting reads (0-100)")

	flag.Parse()

	readCfg := harness.ReadConfig{
		NumReaders: *readers,
		ReadRate:   *readRate,
		ReadDist:   *readDist,
		StartPct:   *readStartPct,
	}

	// Map workload modes to key cardinalities
	var numKeys int
	switch *mode {
	case "A":
		numKeys = 10
	case "B":
		numKeys = 1000000
	case "C":
		numKeys = 100000
	default:
		log.Fatalf("Invalid mode: %s. Must be A, B, or C", *mode)
	}

	var engines []string
	if *enginesStr != "" {
		for _, e := range strings.Split(*enginesStr, ",") {
			t := strings.TrimSpace(e)
			if t != "" {
				engines = append(engines, t)
			}
		}
	}

	var chunkSizes []uint64
	if *chunkSizesStr != "" {
		for _, s := range strings.Split(*chunkSizesStr, ",") {
			t := strings.TrimSpace(s)
			if t == "" {
				continue
			}
			sz, err := strconv.ParseUint(t, 10, 64)
			if err != nil {
				log.Fatalf("Invalid chunk size %q: %v", t, err)
			}
			chunkSizes = append(chunkSizes, sz)
		}
	}

	numBatches := *entries / *batchSize
	if numBatches <= 0 {
		log.Fatalf("Total entries (%d) must be greater than batch size (%d)", *entries, *batchSize)
	}

	if *entries%*batchSize != 0 {
		log.Printf("Warning: entries (%d) is not a multiple of batch_size (%d). Running %d entries instead.",
			*entries, *batchSize, numBatches**batchSize)
	}

	fmt.Printf("Running benchmarks with Mode %s (%d keys), %d entries, batch size %d (%d batches)\n",
		*mode, numKeys, *entries, *batchSize, numBatches)

	var results []benchmarkResult

	ctx := context.Background()

	for _, eng := range engines {
		eng = strings.TrimSpace(eng)
		switch eng {
		case "flat":
			res := runFlat(ctx, *dbDir, *mode, numKeys, numBatches, *batchSize, readCfg)
			results = append(results, res)
		case "log":
			res := runLog(ctx, *dbDir, *mode, numKeys, numBatches, *batchSize, readCfg)
			results = append(results, res)
		case "chunk":
			for _, sz := range chunkSizes {
				res := runChunk(ctx, *dbDir, sz, *mode, numKeys, numBatches, *batchSize, readCfg)
				results = append(results, res)
			}
		case "chunk_scan":
			for _, sz := range chunkSizes {
				res := runChunkScan(ctx, *dbDir, sz, *mode, numKeys, numBatches, *batchSize, readCfg)
				results = append(results, res)
			}
		case "sealing_chunk":
			for _, sz := range chunkSizes {
				res := runSealingChunk(ctx, *dbDir, sz, *mode, numKeys, numBatches, *batchSize, readCfg)
				results = append(results, res)
			}
		case "sealing_chunk_scan":
			for _, sz := range chunkSizes {
				res := runSealingChunkScan(ctx, *dbDir, sz, *mode, numKeys, numBatches, *batchSize, readCfg)
				results = append(results, res)
			}
		default:
			log.Printf("Unknown engine: %s", eng)
		}
	}

	printResults(results)
}

func runFlat(ctx context.Context, baseDir, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	dir := filepath.Join(baseDir, "flat")
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: "flat", err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: "flat", err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewFlatStore(d)
	})
	return benchmarkResult{engine: "flat", results: res, err: err, numReaders: readCfg.NumReaders}
}

func runLog(ctx context.Context, baseDir, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	dir := filepath.Join(baseDir, "log")
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: "log", err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: "log", err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewLogStore(d)
	})
	return benchmarkResult{engine: "log", results: res, err: err, numReaders: readCfg.NumReaders}
}

func runChunk(ctx context.Context, baseDir string, chunkSize uint64, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	name := fmt.Sprintf("chunk (size=%d)", chunkSize)
	dir := filepath.Join(baseDir, fmt.Sprintf("chunk_%d", chunkSize))
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewChunkStore(d, chunkSize)
	})
	return benchmarkResult{engine: name, results: res, err: err, numReaders: readCfg.NumReaders}
}

func runChunkScan(ctx context.Context, baseDir string, chunkSize uint64, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	name := fmt.Sprintf("chunk_scan (size=%d)", chunkSize)
	dir := filepath.Join(baseDir, fmt.Sprintf("chunk_scan_%d", chunkSize))
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewChunkScanStore(d, chunkSize)
	})
	return benchmarkResult{engine: name, results: res, err: err, numReaders: readCfg.NumReaders}
}

func runSealingChunk(ctx context.Context, baseDir string, chunkSize uint64, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	name := fmt.Sprintf("sealing_chunk (size=%d)", chunkSize)
	dir := filepath.Join(baseDir, fmt.Sprintf("sealing_chunk_%d", chunkSize))
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewSealingChunkStore(d, chunkSize)
	})
	return benchmarkResult{engine: name, results: res, err: err, numReaders: readCfg.NumReaders}
}

func runSealingChunkScan(ctx context.Context, baseDir string, chunkSize uint64, mode string, numKeys, numBatches, batchSize int, readCfg harness.ReadConfig) benchmarkResult {
	name := fmt.Sprintf("sealing_chunk_scan (size=%d)", chunkSize)
	dir := filepath.Join(baseDir, fmt.Sprintf("sealing_chunk_scan_%d", chunkSize))
	if err := os.RemoveAll(dir); err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to clean dir %s: %w", dir, err)}
	}
	defer os.RemoveAll(dir)

	gen, err := harness.NewGenerator(mode, numKeys)
	if err != nil {
		return benchmarkResult{engine: name, err: fmt.Errorf("failed to create generator: %w", err)}
	}

	res, err := harness.RunBenchmark(ctx, dir, gen, numBatches, batchSize, numKeys, readCfg, func(d string) (store.IndexStore, error) {
		return store.NewSealingChunkScanStore(d, chunkSize)
	})
	return benchmarkResult{engine: name, results: res, err: err, numReaders: readCfg.NumReaders}
}

func printResults(results []benchmarkResult) {
	fmt.Println("\n| Engine | Write QPS | Write p50 | Write p99 | Write Max | Read QPS | Read p50 | Read p99 | Read Max | Read Errors | Size Before | Size After |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("| %s | ERROR: %v | | | | | | | | | | |\n", r.engine, r.err)
			continue
		}

		readQPS := "N/A"
		readP50 := "N/A"
		readP99 := "N/A"
		readMax := "N/A"
		readErrors := "N/A"

		if r.numReaders > 0 {
			readQPS = fmt.Sprintf("%.2f", r.results.ReadThroughput)
			readP50 = fmt.Sprintf("%v", r.results.ReadP50)
			readP99 = fmt.Sprintf("%v", r.results.ReadP99)
			readMax = fmt.Sprintf("%v", r.results.ReadMax)
			readErrors = fmt.Sprintf("%d", r.results.ReadErrors)
		}

		fmt.Printf("| %s | %.2f | %v | %v | %v | %s | %s | %s | %s | %s | %s | %s |\n",
			r.engine,
			r.results.Throughput,
			r.results.P50Latency,
			r.results.P99Latency,
			r.results.MaxLatency,
			readQPS,
			readP50,
			readP99,
			readMax,
			readErrors,
			formatBytes(r.results.SizeBeforeBytes),
			formatBytes(r.results.SizeAfterBytes),
		)
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
