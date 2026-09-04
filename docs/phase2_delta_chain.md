# Phase 2: Bw-Tree Delta Chain & Auto-Consolidation

## Architectural Rationale
Phase 1 revealed an $O(N)$ write-amplification bottleneck in flat leaf nodes due to continuous slice shifting (`copy()`) during reverse insertions. Phase 2 introduces an immutable base leaf wrapped by a singly-linked `DeltaNode` chain to achieve $O(1)$ writes.

## Benchmark Results

| Architecture | Workload | Metric | Value |
| :--- | :--- | :--- | :--- |
| **Phase 1 (Flat Leaf)** | 1k capacity, Reverse Put | Write Latency | `1,828 ns/op` |
| **Phase 1 (Flat Leaf)** | 1M capacity, Reverse Put | Write Latency | `551,575 ns/op` |
| **Phase 2 (Delta Chain)** | Unbounded Chain, Reverse Put | Write Latency | `281.5 ns/op` |
| **Phase 2 (Delta Chain)** | 100 deltas + 1k base, Read (`Get`) | Read Latency | `2,662 ns/op` |
| **Phase 2 (Auto-Compaction)** | Threshold = 8, 1M base, Reverse Put | Write Latency | `2,822,211 ns/op` |

## Key Insights
1. **Write Acceleration:** Delta prepend drops raw write latency by ~2,000x over the 1M flat leaf baseline.
2. **Read Amplification:** Reads must traverse the delta chain before falling back to the base page, increasing read latency to `2,662 ns/op`.
3. **Compaction Bottleneck:** Rebuilding an unbounded base page (1M items) every 8 writes creates massive write amplification (`2.82 ms/op`, 5.97 MB/op). Pages must be bounded in size via **Page Splitting** to keep compaction costs constant.