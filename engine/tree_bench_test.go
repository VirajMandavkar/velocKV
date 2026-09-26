package engine

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func BenchmarkTree_SlottedPage(b *testing.B) {
	sizes := []int{1000, 10000, 100000, 1000000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {

			b.Run("Put_Sequential", func(b *testing.B) {
				keys := make([][]byte, n)
				for j := 0; j < n; j++ {
					keys[j] = []byte(fmt.Sprintf("key-%08d", j))
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j], 0)
					}
				}
			})

			b.Run("Put_Random", func(b *testing.B) {
				keys := make([][]byte, n)
				for j := 0; j < n; j++ {
					keys[j] = []byte(fmt.Sprintf("key-%08d", j))
				}
				rand.Shuffle(n, func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j], 0)
					}
				}
			})

			b.Run("Put_UpdateHeavy", func(b *testing.B) {
				keyspace := n / 10
				if keyspace < 1 {
					keyspace = 1
				}

				type kv struct{ k, v []byte }
				ops := make([]kv, n)
				for j := 0; j < n; j++ {
					ops[j] = kv{
						k: []byte(fmt.Sprintf("key-%08d", j%keyspace)),
						v: []byte(fmt.Sprintf("v%d", j)),
					}
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(ops[j].k, ops[j].v, 0)
					}
				}
			})

			b.Run("Get_Hot", func(b *testing.B) {
				tree := NewTree()
				keys := make([][]byte, n)
				for i := 0; i < n; i++ {
					keys[i] = []byte(fmt.Sprintf("key-%08d", i))
					tree.Put(keys[i], keys[i], 0)
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					k := keys[i%n]
					_, _, found := tree.Get(k, 0)
					if !found {
						b.Fatalf("missing key: %s", k)
					}
				}
			})

			b.Run("Delete_ThenConsolidate", func(b *testing.B) {
				tree := NewTree()
				keys := make([][]byte, n)
				for i := 0; i < n; i++ {
					keys[i] = []byte(fmt.Sprintf("key-%08d", i))
					tree.Put(keys[i], keys[i], 0)
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					k := keys[i%n]
					tree.Delete(k, 0)
				}
			})

			b.Run("Scan_Ranges", func(b *testing.B) {
				tree := NewTree()
				keys := make([][]byte, n)
				for i := 0; i < n; i++ {
					keys[i] = []byte(fmt.Sprintf("key-%08d", i))
					tree.Put(keys[i], keys[i], 0)
				}

				scanSize := 100
				if scanSize > n {
					scanSize = n
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					startIdx := i % (n - scanSize + 1)
					startKey := keys[startIdx]
					endKey := []byte(fmt.Sprintf("key-%08d", startIdx+scanSize))

					results := tree.Scan(startKey, endKey, 0)
					if len(results) != scanSize {
						b.Fatalf("expected %d results, got %d", scanSize, len(results))
					}
				}
			})
		})
	}
}

func BenchmarkTree_Concurrent(b *testing.B) {
	sizes := []int{100_000, 1_000_000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			keys := make([][]byte, n)
			for j := 0; j < n; j++ {
				keys[j] = []byte(fmt.Sprintf("key-%08d", j))
			}

			b.Run("Put_Random_Parallel", func(b *testing.B) {
				tree := NewTree() // Shared tree for all goroutines

				b.ResetTimer()
				b.ReportAllocs()

				b.RunParallel(func(pb *testing.PB) {
					// Register this specific goroutine with the EBR manager
					threadID := tree.RegisterThread()

					seed := time.Now().UnixNano()
					localRand := rand.New(rand.NewSource(seed))

					for pb.Next() {
						k := keys[localRand.Intn(n)]
						tree.Put(k, k, threadID) // Pass threadID into the engine
					}
				})
			})
		})
	}
}

func BenchmarkTree_MixedWorkload_Parallel(b *testing.B) {
	sizes := []int{100_000, 1_000_000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			// 1. Initialize a single shared tree and pre-allocate keys
			tree := NewTree()
			keys := make([][]byte, n)
			for j := 0; j < n; j++ {
				keys[j] = []byte(fmt.Sprintf("key-%08d", j))
			}

			// 2. Pre-populate 50% of the tree so Gets/Deletes/Scans have data to hit
			for j := 0; j < n/2; j++ {
				tree.Put(keys[j], keys[j], 0)
			}

			b.ResetTimer()
			b.ReportAllocs()

			b.RunParallel(func(pb *testing.PB) {
				threadID := tree.RegisterThread()

				// Seed the local PRNG uniquely per thread to avoid contention
				seed := time.Now().UnixNano() + int64(threadID)
				localRand := rand.New(rand.NewSource(seed))

				for pb.Next() {
					// Roll a 100-sided die to determine the operation
					op := localRand.Intn(100)
					kIdx := localRand.Intn(n)
					key := keys[kIdx]

					if op < 40 {
						// 40% Puts (mix of inserts and in-place updates)
						tree.Put(key, key, threadID)
					} else if op < 80 {
						// 40% Gets (point lookups)
						tree.Get(key, threadID)
					} else if op < 95 {
						// 15% Deletes (triggers tombstones and Consolidate)
						tree.Delete(key, threadID)
					} else {
						// 5% Scans (short horizontal B-Link traversals)
						startIdx := kIdx
						endIdx := startIdx + 50
						if endIdx >= n {
							endIdx = n - 1
						}
						if startIdx < endIdx {
							tree.Scan(keys[startIdx], keys[endIdx], threadID)
						}
					}
				}
			})
		})
	}
}
