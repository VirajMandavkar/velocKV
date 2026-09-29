package engine

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Correctness guards. Run outside the timer. A benchmark whose tree lost or
// corrupted data reports FAIL instead of a number: a dropped write is a cheap
// write, so an unguarded ns/op would flatter v2.
// ---------------------------------------------------------------------------

// verifyKeys checks every keys[i] maps to vals[i].
func verifyKeys(b *testing.B, tree *Tree, keys, vals [][]byte) {
	b.Helper()
	lost, wrong := 0, 0
	for i, k := range keys {
		v, _, ok := tree.Get(k, 0)
		switch {
		case !ok:
			lost++
		case !bytes.Equal(v, vals[i]):
			wrong++
		}
	}
	if lost+wrong > 0 {
		b.Fatalf("CORRECTNESS: %d lost, %d wrong of %d keys", lost, wrong, len(keys))
	}
}

// verifyScan does one full-range scan: keys strictly increasing (no duplicates,
// no reordering), every value == its key, and optionally an exact count.
func verifyScan(b *testing.B, tree *Tree, wantCount int) {
	b.Helper()
	res := tree.Scan([]byte("key-"), []byte("key-~"), 0) // '~' sorts after digits
	for i, kv := range res {
		if i > 0 && bytes.Compare(res[i-1].Key, kv.Key) >= 0 {
			b.Fatalf("CORRECTNESS: scan order broken at %d: %q then %q", i, res[i-1].Key, kv.Key)
		}
		if !bytes.Equal(kv.Key, kv.Val) {
			b.Fatalf("CORRECTNESS: key %q has value %q", kv.Key, kv.Val)
		}
	}
	if wantCount >= 0 && len(res) != wantCount {
		b.Fatalf("CORRECTNESS: scan returned %d keys, want %d", len(res), wantCount)
	}
}

func makeKeys(n int) [][]byte {
	keys := make([][]byte, n)
	for j := range keys {
		keys[j] = []byte(fmt.Sprintf("key-%08d", j))
	}
	return keys
}

func BenchmarkTree_SlottedPage(b *testing.B) {
	sizes := []int{1000, 10000, 100000, 1000000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {

			b.Run("Put_Sequential", func(b *testing.B) {
				keys := makeKeys(n)
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j], 0)
					}
					if i == b.N-1 {
						b.StopTimer()
						verifyKeys(b, tree, keys, keys)
						verifyScan(b, tree, n)
						b.StartTimer()
					}
					tree.Close() // P39
				}
			})

			b.Run("Put_Random", func(b *testing.B) {
				keys := makeKeys(n)
				rand.Shuffle(n, func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j], 0)
					}
					if i == b.N-1 {
						b.StopTimer()
						verifyKeys(b, tree, keys, keys)
						verifyScan(b, tree, n)
						b.StartTimer()
					}
					tree.Close() // P39
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
				// Expected final state: last write per key wins.
				last := make(map[string][]byte, keyspace)
				for _, op := range ops {
					last[string(op.k)] = op.v
				}
				wantK := make([][]byte, 0, keyspace)
				wantV := make([][]byte, 0, keyspace)
				for k, v := range last {
					wantK = append(wantK, []byte(k))
					wantV = append(wantV, v)
				}

				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					tree := NewTree()
					for j := 0; j < n; j++ {
						tree.Put(ops[j].k, ops[j].v, 0)
					}
					if i == b.N-1 {
						b.StopTimer()
						verifyKeys(b, tree, wantK, wantV)
						b.StartTimer()
					}
					tree.Close() // P39
				}
			})

			b.Run("Get_Hot", func(b *testing.B) {
				keys := makeKeys(n)
				tree := NewTree()
				defer tree.Close() // P39
				for i := 0; i < n; i++ {
					tree.Put(keys[i], keys[i], 0)
				}
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					k := keys[i%n]
					v, _, found := tree.Get(k, 0)
					if !found || !bytes.Equal(v, k) {
						b.Fatalf("CORRECTNESS: bad Get for %s", k)
					}
				}
			})

			b.Run("Delete_ThenConsolidate", func(b *testing.B) {
				keys := makeKeys(n)
				var tree *Tree
				rebuild := func() {
					if tree != nil {
						tree.Close() // P39
					}
					tree = NewTree()
					for j := 0; j < n; j++ {
						tree.Put(keys[j], keys[j], 0)
					}
				}
				rebuild()
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if i > 0 && i%n == 0 { // every key deleted: rebuild, else we time no-op deletes
						b.StopTimer()
						rebuild()
						b.StartTimer()
					}
					if !tree.Delete(keys[i%n], 0) {
						b.Fatalf("CORRECTNESS: Delete(%s) returned false on a live key", keys[i%n])
					}
				}
				b.StopTimer()
				// Keys deleted in the current round must be gone; the rest must remain.
				done := (b.N-1)%n + 1
				verifyKeys(b, tree, keys[done:], keys[done:])
				for _, k := range keys[:done] {
					if _, _, ok := tree.Get(k, 0); ok {
						b.Fatalf("CORRECTNESS: deleted key %s still visible", k)
					}
				}
				verifyScan(b, tree, n-done)
				tree.Close()
			})

			b.Run("Scan_Ranges", func(b *testing.B) {
				keys := makeKeys(n)
				tree := NewTree()
				defer tree.Close() // P39
				for i := 0; i < n; i++ {
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
						b.Fatalf("CORRECTNESS: expected %d results, got %d", scanSize, len(results))
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
			keys := makeKeys(n)

			b.Run("Put_Random_Parallel", func(b *testing.B) {
				tree := NewTree()
				defer tree.Close() // P39

				b.ResetTimer()
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					threadID := tree.RegisterThread()
					localRand := rand.New(rand.NewSource(time.Now().UnixNano()))
					for pb.Next() {
						k := keys[localRand.Intn(n)]
						tree.Put(k, k, threadID)
					}
				})
				b.StopTimer()
				verifyScan(b, tree, -1) // unknown count; order, no dups, values intact
			})
		})
	}
}

func BenchmarkTree_MixedWorkload_Parallel(b *testing.B) {
	sizes := []int{100_000, 1_000_000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			tree := NewTree()
			defer tree.Close() // P39
			keys := makeKeys(n)
			for j := 0; j < n/2; j++ {
				tree.Put(keys[j], keys[j], 0)
			}

			b.ResetTimer()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				threadID := tree.RegisterThread()
				localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(threadID)))
				for pb.Next() {
					op := localRand.Intn(100)
					kIdx := localRand.Intn(n)
					key := keys[kIdx]
					switch {
					case op < 40:
						tree.Put(key, key, threadID)
					case op < 80:
						tree.Get(key, threadID)
					case op < 95:
						tree.Delete(key, threadID)
					default:
						endIdx := kIdx + 50
						if endIdx >= n {
							endIdx = n - 1
						}
						if kIdx < endIdx {
							tree.Scan(keys[kIdx], keys[endIdx], threadID)
						}
					}
				}
			})
			b.StopTimer()
			verifyScan(b, tree, -1)
		})
	}
}
