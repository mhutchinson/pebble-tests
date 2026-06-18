package harness

import (
	"context"
	"os"
	"testing"

	"github.com/mhutchinson/pebble-tests/store"
)

func TestRunBenchmark(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pebble-benchmark-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	gen, err := NewGenerator("A", 100)
	if err != nil {
		t.Fatalf("failed to create generator: %v", err)
	}

	numBatches := 5
	batchSize := 10

	results, err := RunBenchmark(context.Background(), tmpDir, gen, numBatches, batchSize, func(d string) (store.IndexStore, error) {
		return store.NewFlatStore(d)
	})
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if results.Throughput <= 0 {
		t.Errorf("expected throughput > 0, got %f", results.Throughput)
	}
	if results.P50Latency <= 0 {
		t.Errorf("expected P50 latency > 0, got %v", results.P50Latency)
	}
	if results.P99Latency <= 0 {
		t.Errorf("expected P99 latency > 0, got %v", results.P99Latency)
	}
	if results.MaxLatency <= 0 {
		t.Errorf("expected Max latency > 0, got %v", results.MaxLatency)
	}
	if results.SizeBeforeBytes <= 0 {
		t.Errorf("expected SizeBeforeBytes > 0, got %d", results.SizeBeforeBytes)
	}
	if results.SizeAfterBytes <= 0 {
		t.Errorf("expected SizeAfterBytes > 0, got %d", results.SizeAfterBytes)
	}

	t.Logf("Results: %+v", results)
}
