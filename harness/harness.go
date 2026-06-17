package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mhutchinson/pebble-tests/store"
)

// Results holds the benchmark metrics.
type Results struct {
	Throughput      float64 // indices per second
	P50Latency      time.Duration
	P99Latency      time.Duration
	MaxLatency      time.Duration
	SizeBeforeBytes int64
	SizeAfterBytes  int64
}

// RunBenchmark runs the benchmark on the given store.
// dbDir is needed to measure the size.
func RunBenchmark(ctx context.Context, s store.IndexStore, dbDir string, gen *Generator, numBatches int, batchSize int) (*Results, error) {
	latencies := make([]time.Duration, numBatches)
	startIdx := uint64(0)

	totalStart := time.Now()
	for i := 0; i < numBatches; i++ {
		batch := gen.NextBatch(batchSize, startIdx)
		startIdx += uint64(batchSize)

		batchStart := time.Now()
		err := s.WriteBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("failed to write batch %d: %w", i, err)
		}
		latencies[i] = time.Since(batchStart)
	}
	totalDuration := time.Since(totalStart)

	// Calculate throughput
	totalIndices := float64(numBatches * batchSize)
	throughput := totalIndices / totalDuration.Seconds()

	// Calculate latencies
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[numBatches/2]
	p99 := latencies[int(float64(numBatches)*0.99)]
	max := latencies[numBatches-1]

	// Measure size before compaction
	sizeBefore, err := dirSize(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to measure size before compaction: %w", err)
	}

	// Compact
	compactStart := time.Now()
	if err := s.Compact(); err != nil {
		return nil, fmt.Errorf("failed to compact store: %w", err)
	}
	fmt.Printf("Compaction took %v\n", time.Since(compactStart))

	// Measure size after compaction
	sizeAfter, err := dirSize(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to measure size after compaction: %w", err)
	}

	return &Results{
		Throughput:      throughput,
		P50Latency:      p50,
		P99Latency:      p99,
		MaxLatency:      max,
		SizeBeforeBytes: sizeBefore,
		SizeAfterBytes:  sizeAfter,
	}, nil
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}
