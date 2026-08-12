package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhutchinson/pebble-tests/store"
)

// ReadConfig configures the concurrent readers.
type ReadConfig struct {
	NumReaders int
	ReadRate   int     // total QPS target (<= 0 for unlimited)
	ReadDist   string  // "uniform", "zipfian", "recent"
	StartPct   float64 // percentage of writes completed before starting reads (0-100)
}

// Results holds the benchmark metrics.
type Results struct {
	Throughput      float64 // indices per second
	P50Latency      time.Duration
	P99Latency      time.Duration
	MaxLatency      time.Duration
	SizeBeforeBytes int64
	SizeAfterBytes  int64

	// Read metrics
	ReadThroughput float64
	ReadP50        time.Duration
	ReadP99        time.Duration
	ReadMax        time.Duration
	ReadErrors     int64
}

// RunBenchmark runs the benchmark on the given store.
func RunBenchmark(
	ctx context.Context,
	dbDir string,
	gen *Generator,
	numBatches int,
	batchSize int,
	numKeys int,
	readCfg ReadConfig,
	newStore func(dir string) (store.IndexStore, error),
) (*Results, error) {
	latencies := make([]time.Duration, numBatches)
	startIdx := uint64(0)

	s, err := newStore(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	var writerProgress uint64
	done := make(chan struct{})
	var wg sync.WaitGroup

	readCtx, cancelReads := context.WithCancel(ctx)

	type readerStats struct {
		latencies []time.Duration
		errors    int64
	}
	stats := make([]readerStats, readCfg.NumReaders)

	var limiter *tokenBucket
	if readCfg.ReadRate > 0 {
		limiter = newTokenBucket(float64(readCfg.ReadRate))
	}

	if readCfg.NumReaders > 0 {
		readMode := ""
		switch readCfg.ReadDist {
		case "uniform":
			readMode = "A"
		case "recent":
			readMode = "B"
		case "zipfian":
			readMode = "C"
		default:
			cancelReads()
			s.Close()
			return nil, fmt.Errorf("invalid read distribution: %s", readCfg.ReadDist)
		}

		for i := 0; i < readCfg.NumReaders; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				rg, err := NewReadGenerator(readMode, numKeys, &writerProgress)
				if err != nil {
					fmt.Printf("reader %d failed to create generator: %v\n", id, err)
					return
				}

				if readCfg.StartPct > 0 {
					targetVal := uint64(float64(numBatches*batchSize) * (readCfg.StartPct / 100.0))
					for {
						if atomic.LoadUint64(&writerProgress) >= targetVal {
							break
						}
						select {
						case <-readCtx.Done():
							return
						case <-time.After(10 * time.Millisecond):
						}
					}
				}

				var localLatencies []time.Duration
				var localErrors int64

				for {
					select {
					case <-readCtx.Done():
						stats[id] = readerStats{latencies: localLatencies, errors: localErrors}
						return
					default:
					}

					if limiter != nil {
						if err := limiter.Wait(readCtx); err != nil {
							if readCtx.Err() != nil {
								stats[id] = readerStats{latencies: localLatencies, errors: localErrors}
								return
							}
							localErrors++
							continue
						}
					}

					hash, start := rg.NextQuery()
					readStart := time.Now()
					_, err := s.Lookup(readCtx, hash, start)
					duration := time.Since(readStart)

					if err != nil {
						if readCtx.Err() != nil {
							stats[id] = readerStats{latencies: localLatencies, errors: localErrors}
							return
						}
						localErrors++
					} else {
						localLatencies = append(localLatencies, duration)
					}
				}
			}(i)
		}
	}

	totalStart := time.Now()
	for i := 0; i < numBatches; i++ {
		batch := gen.NextBatch(batchSize, startIdx)
		startIdx += uint64(batchSize)

		batchStart := time.Now()
		err := s.WriteBatch(ctx, batch)
		if err != nil {
			cancelReads()
			close(done)
			wg.Wait()
			s.Close()
			return nil, fmt.Errorf("failed to write batch %d: %w", i, err)
		}
		latencies[i] = time.Since(batchStart)
		atomic.StoreUint64(&writerProgress, startIdx)
	}
	totalDuration := time.Since(totalStart)

	cancelReads()
	close(done)
	wg.Wait()

	// Close store to flush WAL and ensure clean state before measuring size.
	if err := s.Close(); err != nil {
		return nil, fmt.Errorf("failed to close store before measuring: %w", err)
	}

	// Measure size before compaction
	sizeBefore, err := dirSize(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to measure size before compaction: %w", err)
	}

	// Reopen store for compaction
	s, err = newStore(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to reopen store for compaction: %w", err)
	}

	// Compact
	compactStart := time.Now()
	if err := s.Compact(); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to compact store: %w", err)
	}
	fmt.Printf("Compaction took %v\n", time.Since(compactStart))

	// Close store again to ensure all compaction writes are flushed and obsolete files are deleted.
	if err := s.Close(); err != nil {
		return nil, fmt.Errorf("failed to close store after compaction: %w", err)
	}

	// Measure size after compaction
	sizeAfter, err := dirSize(dbDir)
	if err != nil {
		return nil, fmt.Errorf("failed to measure size after compaction: %w", err)
	}

	// Calculate throughput
	totalIndices := float64(numBatches * batchSize)
	throughput := totalIndices / totalDuration.Seconds()

	// Calculate latencies
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[numBatches/2]
	p99 := latencies[int(float64(numBatches)*0.99)]
	max := latencies[numBatches-1]

	// Aggregate read metrics
	var allReadLatencies []time.Duration
	var totalReadErrors int64
	for _, s := range stats {
		allReadLatencies = append(allReadLatencies, s.latencies...)
		totalReadErrors += s.errors
	}

	var readThroughput float64
	var readP50, readP99, readMax time.Duration

	if len(allReadLatencies) > 0 {
		readThroughput = float64(len(allReadLatencies)) / totalDuration.Seconds()
		sort.Slice(allReadLatencies, func(i, j int) bool { return allReadLatencies[i] < allReadLatencies[j] })
		readP50 = allReadLatencies[len(allReadLatencies)/2]
		readP99 = allReadLatencies[int(float64(len(allReadLatencies))*0.99)]
		readMax = allReadLatencies[len(allReadLatencies)-1]
	}

	return &Results{
		Throughput:      throughput,
		P50Latency:      p50,
		P99Latency:      p99,
		MaxLatency:      max,
		SizeBeforeBytes: sizeBefore,
		SizeAfterBytes:  sizeAfter,
		ReadThroughput:  readThroughput,
		ReadP50:         readP50,
		ReadP99:         readP99,
		ReadMax:         readMax,
		ReadErrors:      totalReadErrors,
	}, nil
}

type tokenBucket struct {
	rate       float64
	burst      float64
	tokens     float64
	lastRefill time.Time
	mu         sync.Mutex
}

func newTokenBucket(rate float64) *tokenBucket {
	burst := rate / 10
	if burst < 1 {
		burst = 1
	}
	return &tokenBucket{
		rate:       rate,
		burst:      burst,
		tokens:     0, // start empty
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) Wait(ctx context.Context) error {
	tb.mu.Lock()
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.burst {
		tb.tokens = tb.burst
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		tb.mu.Unlock()
		return nil
	}

	needed := 1.0 - tb.tokens
	waitDur := time.Duration(needed / tb.rate * float64(time.Second))
	tb.tokens -= 1.0 // reserve the token
	tb.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(waitDur):
		return nil
	}
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
