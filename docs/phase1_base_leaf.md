1. **Why Parallel [][]byte Slices Over Pointer-per-Key Allocations**

- *Cache Locality*: Contiguous memory arrays (slices) keep keys and values packed tightly in RAM. The CPU fetches whole blocks into cache at once. A pointer-per-key architecture induces random heap-allocations, scattering data across RAM and triggering CPU cache misses during sequential scans.

- *Zero Garbage Collection (GC) Pointer Overhead*: Every unique pointer forces the Go runtime garbage collector to trace its reference map. Storing values as contiguous slices heavily minimizes the total object count, drastically reducing GC pause times.

- *Reduced Memory Footprint*: Parallel slices eliminate the metadata overhead (typically 8 bytes for a pointer per node) required to link child nodes together individually, optimizing physical memory density.


2. **Big-O Complexity of a Mid-Leaf Insertion**

- *Exact Time Complexity:* \(\mathcal{O}(N)\) where \(N\) is the current number of items in the leaf.

- *The Core Bottleneck*: While finding the position takes \(\mathcal{O}(N)\) or \(\mathcal{O}(\log N)\) via scan/binary search, your current implementation relies on shifting existing memory via copy().

- *The Physical Shift*: To inject a key into the middle of the leaf, you must move all subsequent elements (\(N - \text{insertIdx}\)) down by one memory slot. For large capacities, this memory-copy loop blocks performance, making it the explicit target for optimization in Phase 2.