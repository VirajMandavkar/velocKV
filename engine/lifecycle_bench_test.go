package engine

import (
	"fmt"
	"math/rand"
	"testing"
)

// BenchmarkTree_FullLifecycle exercises every code path implemented so far —
// sequential and random Put (forcing leaf Split and Consolidation), an
// update-heavy workload that hammers the delta-chain dedup logic, Get
// (hot-key reads through the delta chain), Delete (tombstones +
// consolidation), and Scan (multi-leaf range scans) — across a few tree
// sizes so you can see how each phase scales.
//
// Run everything:
//
//	go test -bench=BenchmarkTree_FullLifecycle -benchmem
//
// Run one phase at one size:
//
//	go test -bench='BenchmarkTree_FullLifecycle/n=10000/Scan_Ranges' -benchmem
//
// NOTE: the n=100_000 case assumes NewLeafNode no longer preallocates
// 1,000,000-capacity slices per leaf. If you haven't applied that fix yet,
// drop 100_000 from the sizes slice below or this will OOM exactly like
// BenchmarkTree_Scan did.
func BenchmarkTree_FullLifecycle(b *testing.B) {
	sizes := []int{1_000, 10_000, 100_000}

	for _, n := range sizes {
		n := n
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.Run("Put_Sequential", func(b *testing.B) {
				benchmarkPutSequential(b, n)
			})
			b.Run("Put_Random", func(b *testing.B) {
				benchmarkPutRandom(b, n)
			})
			b.Run("Put_UpdateHeavy", func(b *testing.B) {
				benchmarkPutUpdateHeavy(b, n)
			})
			b.Run("Get_Hot", func(b *testing.B) {
				benchmarkGetHot(b, n)
			})
			b.Run("Delete_ThenConsolidate", func(b *testing.B) {
				benchmarkDeleteThenConsolidate(b, n)
			})
			b.Run("Scan_Ranges", func(b *testing.B) {
				benchmarkScanRanges(b, n)
			})
		})
	}
}

// benchmarkPutSequential inserts keys in ascending order — the worst case
// for split fan-out since every split happens at the "hot" right edge.
func benchmarkPutSequential(b *testing.B, n int) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tree := &Tree{}
		for j := 0; j < n; j++ {
			key := []byte(fmt.Sprintf("key-%08d", j))
			if err := tree.Put(key, key); err != nil {
				b.Fatalf("put failed at %d: %v", j, err)
			}
		}
	}
}

// benchmarkPutRandom inserts the same keyspace in random order, exercising
// InternalNode.Put's mid-array insert/shift path instead of always
// appending at the edge.
func benchmarkPutRandom(b *testing.B, n int) {
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
	}
	rand.Shuffle(n, func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tree := &Tree{}
		for _, k := range keys {
			if err := tree.Put(k, k); err != nil {
				b.Fatalf("put failed: %v", err)
			}
		}
	}
}

// benchmarkPutUpdateHeavy repeatedly upserts a keyspace 10x smaller than n,
// so the delta chain fills with duplicate keys and both logicalKeyCount's
// dedup pass and Consolidation's seen-map get real work.
func benchmarkPutUpdateHeavy(b *testing.B, n int) {
	keyspace := n / 10
	if keyspace < 1 {
		keyspace = 1
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tree := &Tree{}
		for j := 0; j < n; j++ {
			key := []byte(fmt.Sprintf("key-%08d", j%keyspace))
			val := []byte(fmt.Sprintf("v%d", j))
			if err := tree.Put(key, val); err != nil {
				b.Fatalf("put failed: %v", err)
			}
		}
	}
}

// benchmarkGetHot builds the tree once, then benchmarks pure read
// throughput across the whole keyspace (post-consolidation, so it's
// hitting base-leaf binary-search territory rather than delta chains).
func benchmarkGetHot(b *testing.B, n int) {
	tree := &Tree{}
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("key-%08d", i))
		if err := tree.Put(keys[i], keys[i]); err != nil {
			b.Fatalf("setup put failed: %v", err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		k := keys[i%n]
		val, found := tree.Get(k)
		if !found || string(val) != string(k) {
			b.Fatalf("unexpected result for %s: found=%v val=%s", k, found, val)
		}
	}
}

// benchmarkDeleteThenConsolidate builds a tree, then times deleting every
// other key. Each ChainNode.Delete pushes a tombstone onto its own delta
// chain and can trigger that leaf's Consolidation independently — this
// exercises tombstone-honoring across whatever leaves the keys landed in
// after Split, not just a single ChainNode like the unit tests do.
func benchmarkDeleteThenConsolidate(b *testing.B, n int) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tree := &Tree{}
		keys := make([][]byte, n)
		for j := 0; j < n; j++ {
			keys[j] = []byte(fmt.Sprintf("key-%08d", j))
			if err := tree.Put(keys[j], keys[j]); err != nil {
				b.Fatalf("setup put failed: %v", err)
			}
		}
		b.StartTimer()

		for j, k := range keys {
			if j%2 != 0 {
				continue
			}
			leaf := tree.root.FindLeaf(k)
			if leaf == nil {
				b.Fatalf("FindLeaf returned nil for key %s", k)
			}
			leaf.Delete(k)
		}

		b.StopTimer()
		for j, k := range keys {
			_, found := tree.Get(k)
			if j%2 == 0 && found {
				b.Fatalf("key %s should be deleted but was found", k)
			}
			if j%2 != 0 && !found {
				b.Fatalf("surviving key %s went missing", k)
			}
		}
		b.StartTimer()
	}
}

// benchmarkScanRanges builds the tree once, then times fixed-width range
// scans starting at random offsets — this walks the leaf.next chain across
// however many ChainNodes the range spans, not just one.
func benchmarkScanRanges(b *testing.B, n int) {
	tree := &Tree{}
	for j := 0; j < n; j++ {
		key := []byte(fmt.Sprintf("key-%08d", j))
		if err := tree.Put(key, key); err != nil {
			b.Fatalf("setup put failed: %v", err)
		}
	}

	rangeSize := 100
	if rangeSize > n {
		rangeSize = n
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		start := rand.Intn(n - rangeSize + 1)
		startKey := []byte(fmt.Sprintf("key-%08d", start))
		endKey := []byte(fmt.Sprintf("key-%08d", start+rangeSize))

		pairs, err := tree.Scan(startKey, endKey)
		if err != nil {
			b.Fatalf("scan failed: %v", err)
		}
		if len(pairs) != rangeSize {
			b.Fatalf("expected %d pairs, got %d", rangeSize, len(pairs))
		}
	}
}
