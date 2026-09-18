package engine

import (
	"fmt"
	"math/rand"
	"testing"
)

func BenchmarkTree_SlottedPage(b *testing.B) {
	sizes := []int{1000, 10000, 100000, 1000000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {

			b.Run("Put_Sequential", func(b *testing.B) {
				// Pre-allocate to eliminate GC artifacts
				keys := make([][]byte, n)
				for j := 0; j < n; j++ {
					keys[j] = []byte(fmt.Sprintf("key-%08d", j))
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := &Tree{}
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j])
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
					tree := &Tree{}
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j])
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
					tree := &Tree{}
					for j := 0; j < n; j++ {
						tree.Put(ops[j].k, ops[j].v)
					}
				}
			})

			b.Run("Get_Hot", func(b *testing.B) {
				tree := &Tree{}
				keys := make([][]byte, n)
				for i := 0; i < n; i++ {
					keys[i] = []byte(fmt.Sprintf("key-%08d", i))
					tree.Put(keys[i], keys[i])
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					k := keys[i%n]
					_, _, found := tree.Get(k)
					if !found {
						b.Fatalf("missing key: %s", k)
					}
				}
			})

			b.Run("Delete_ThenConsolidate", func(b *testing.B) {
				b.Skip("Tree.Delete not yet implemented for SlottedPage architecture")
				// Once implemented, pre-allocate keys and benchmark tree.Delete(k) here.
			})

			b.Run("Scan_Ranges", func(b *testing.B) {
				tree := &Tree{}
				keys := make([][]byte, n)
				for i := 0; i < n; i++ {
					keys[i] = []byte(fmt.Sprintf("key-%08d", i))
					tree.Put(keys[i], keys[i])
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

					results := tree.Scan(startKey, endKey)
					if len(results) != scanSize {
						b.Fatalf("expected %d results, got %d", scanSize, len(results))
					}
				}
			})
		})
	}
}
