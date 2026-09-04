**The Structural Tradeoff: **
- *Why we traded an $O(N)$ base write for an $O(1)$ prepend with an $O(K)$ read penalty (where $K$ is chain length).*

**Tombstone Mechanics: **
- *How delete markers mask underlying immutable records without in-place mutation.*

**Consolidation Cost: **
- *The algorithm for reclaiming memory and resetting read latency by collapsing the delta chain into a fresh base page.*