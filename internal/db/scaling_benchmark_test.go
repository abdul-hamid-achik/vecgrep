package db

import (
	"context"
	"fmt"
	"testing"
)

func openScalingBenchmarkDB(tb testing.TB, dimensions, records int) *DB {
	tb.Helper()

	database, err := OpenWithOptions(OpenOptions{
		Dimensions: dimensions,
		DataDir:    tb.TempDir(),
	})
	if err != nil {
		tb.Fatalf("open database: %v", err)
	}
	tb.Cleanup(func() { _ = database.Close() })

	chunks := make([]ChunkRecord, records)
	embeddings := make([][]float32, records)
	for i := range chunks {
		path := fmt.Sprintf("file-%06d.go", i)
		chunks[i] = NewChunkRecord(
			path, path, fmt.Sprintf("hash-%06d", i), 64,
			"go", "package benchmark", 1, 1, 0, 17,
			"file", "", "/benchmark",
		)
		embeddings[i] = make([]float32, dimensions)
		embeddings[i][i%dimensions] = 1
	}
	if records > 0 {
		if _, err := database.InsertChunkBatch(chunks, embeddings); err != nil {
			tb.Fatalf("insert %d chunks: %v", records, err)
		}
	}
	return database
}

func TestScalingStoreContracts(t *testing.T) {
	for _, dimensions := range []int{8, 64} {
		database := openScalingBenchmarkDB(t, dimensions, 12)
		hashes, err := database.GetFileHashes("/benchmark")
		if err != nil {
			t.Fatalf("dimensions=%d: get file hashes: %v", dimensions, err)
		}
		if len(hashes) != 12 {
			t.Fatalf("dimensions=%d: got %d hashes, want 12", dimensions, len(hashes))
		}
		deleted, err := database.DeleteFile(context.Background(), "file-000011.go")
		if err != nil {
			t.Fatalf("dimensions=%d: delete changed file: %v", dimensions, err)
		}
		if deleted != 1 {
			t.Fatalf("dimensions=%d: deleted %d chunks, want 1", dimensions, deleted)
		}
	}
}

func BenchmarkIncrementalMetadataLookup(b *testing.B) {
	for _, dimensions := range []int{8, 768} {
		for _, records := range []int{1, 64, 256} {
			b.Run(fmt.Sprintf("dimensions=%d/records=%d", dimensions, records), func(b *testing.B) {
				database := openScalingBenchmarkDB(b, dimensions, records)
				hashes, err := database.GetFileHashes("/benchmark")
				if err != nil {
					b.Fatalf("warm incremental metadata: %v", err)
				}
				if len(hashes) != records {
					b.Fatalf("warm lookup got %d hashes, want %d", len(hashes), records)
				}
				b.ReportAllocs()
				b.ReportMetric(float64(records), "records")
				b.ReportMetric(float64(dimensions), "dimensions")
				b.ResetTimer()
				for range b.N {
					hashes, err := database.GetFileHashes("/benchmark")
					if err != nil {
						b.Fatal(err)
					}
					if len(hashes) != records {
						b.Fatalf("got %d hashes, want %d", len(hashes), records)
					}
				}
			})
		}
	}
}

func BenchmarkChangedFileDeletionScaling(b *testing.B) {
	for _, records := range []int{1, 64, 256} {
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(records), "records")
			for range b.N {
				b.StopTimer()
				database := openScalingBenchmarkDB(b, 8, records)
				target := fmt.Sprintf("file-%06d.go", records-1)
				b.StartTimer()
				deleted, err := database.DeleteFile(context.Background(), target)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if deleted != 1 {
					b.Fatalf("deleted %d chunks, want 1", deleted)
				}
				_ = database.Close()
			}
		})
	}
}

func BenchmarkFullDeletionScaling(b *testing.B) {
	for _, records := range []int{1, 64, 256} {
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(records), "records")
			for range b.N {
				b.StopTimer()
				database := openScalingBenchmarkDB(b, 8, records)
				b.StartTimer()
				err := database.ResetAll(context.Background())
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				stats, err := database.Stats()
				if err != nil {
					b.Fatal(err)
				}
				if stats["chunks"] != 0 {
					b.Fatalf("chunks after reset = %d, want 0", stats["chunks"])
				}
				_ = database.Close()
			}
		})
	}
}

func BenchmarkSyncCostByIndexSize(b *testing.B) {
	for _, records := range []int{1, 64, 256} {
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			database := openScalingBenchmarkDB(b, 8, records)
			if err := database.Sync(); err != nil {
				b.Fatalf("initial sync: %v", err)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(records), "records")
			for i := range b.N {
				b.StopTimer()
				if err := database.SetCollectionMetadataValue("benchmark_generation", i); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := database.Sync(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
