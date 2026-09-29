# VelocaKV

An ordered key-value storage engine in pure Go, built from scratch to learn and demonstrate storage-engine internals: page layouts, concurrent B-link trees, optimistic locking, and memory reclamation outside the Go garbage collector.

> **Status:** v2 is a design prototype with known correctness bugs; see [`docs/v2/v2-known-issues.md`](docs/v2/v2-known-issues.md). v3 is in progress; see [`docs/v3/v3-architecture-plan.md`](docs/v3/v3-architecture-plan.md).
>
> Do not store data you care about in v2.

---

## Versions at a glance

| | v1 | v2 | v3 |
|---|---|---|---|
| **State** | Superseded | Prototype on `main`, known bugs | In progress |
| **Structure** | Tree of leaves with Bw-tree-style delta chains | B-link tree of 4 KiB slotted pages | B-link tree of 8 KiB slotted pages |
| **Memory** | Go heap | Go heap | `mmap`'d arena outside the Go heap |
| **Concurrency** | Atomic flag gating consolidation | Optimistic Lock Coupling (version word per page) | OLC with atomic slot reads, bounded retries, restart from root |
| **Deletes** | Tombstones | Tombstones | Slot removal |
| **Reclamation** | Go GC | Go GC (EBR scaffolding, unused) | Epoch-based reclamation with reader lease |
| **Durability** | None | None | WAL + copy-on-write checkpoints (planned) |
| **Docs** | Not yet written | [architecture](docs/v2/v2-architecture.md) · [known issues](docs/v2/v2-known-issues.md) | [architecture plan](docs/v3/v3-architecture-plan.md) |

---

## How the design got here

```
v1  delta chains ──► writes fast, reads slow, consolidation stalls
                          │
                          ▼
v2  slotted pages + B-link + OLC ──► good layout, but every page and scratch
                                     buffer is a Go heap object; GC pressure,
                                     plus 22 correctness bugs found in review
                          │
                          ▼
v3  same page idea, rebuilt on an unmanaged arena with fence keys, key heads,
    atomic slots and real reclamation
```

### v1: delta chains

v1 started with a leaf of flat sorted slices, which hit a write-amplification wall at about **551,575 ns per write** with 1M keys. Prepending Bw-tree-style delta records made writes O(1) at about **281.5 ns**, but reads rose to about **2,662 ns** (walking the chain) and consolidating an unbounded page stalled writes for about **2.82 ms**. The trade-off pointed to a structure whose reads and writes both stay near O(log n) inside a page.

### v2: slotted pages

v2 stores variable-length records in fixed 4 KiB pages: a sorted slot directory grows from the front, record bytes grow from the back, and free space is the gap between them. Pages form a B-link tree, so a split never has to lock its parent, and readers use Optimistic Lock Coupling, so they never write shared memory.

A line-by-line review found **44 issues: 22 bugs (4 critical), 21 design debts, 1 non-issue.** The critical ones can silently lose writes or crash under concurrency. Each is documented with the file, function, trigger, and the v3 change that resolves it. v2 benchmark numbers are also unreliable until the benchmarks stop leaking trees (issue P39).

Read in this order:
1. [`docs/v2/v2-architecture.md`](docs/v2/v2-architecture.md): how v2 works, as built.
2. [`docs/v2/v2-known-issues.md`](docs/v2/v2-known-issues.md): what is wrong with it and how v3 fixes each item.

### v3: zero-GC engine (in progress)

v3 keeps the slotted-page idea and rebuilds everything around it:

- **8 KiB pages** in 64 MiB `mmap`'d slabs addressed by 32-bit page IDs, so the Go collector sees no tree memory. 4 KiB stays buildable (`-tags page4k`) and a fixed benchmark decides between them.
- **64-byte header** (one cache line) with stored low and high fence keys; the key prefix is derived from the fences.
- **8-byte atomic slots** carrying a 3-byte key head, so most binary-search steps never leave the slot directory.
- **Deletes remove the slot**; empty values are legal.
- **Lazy compaction before split**, byte-median separators, lazy same-parent merges.
- **Readers** bounds-check every offset, validate every answer, and restart from the root on any dead or recycled page.
- **Zero-allocation API** through per-worker handles.

Full design, decisions and reasoning: [`docs/v3/v3-architecture-plan.md`](docs/v3/v3-architecture-plan.md).

---

## v3 roadmap

Each phase ends with a gate that must pass before the next begins.

| # | Phase | Gate | Status |
|---|---|---|---|
| 1 | Page format: header, slots with heads, fences, overflow records, lazy compaction, page invariant checker | Page fuzzing clean under 4 and 8 KiB builds; torn-slot test never reads out of bounds | In progress |
| 2 | Arena and allocator: `mmap` slabs, sharded free lists, per-worker registration | 5M concurrent alloc/free at 0 allocs/op; race detector clean | Planned |
| 3 | Single-threaded tree: inner pages, splits, merges, zero-alloc API, scans | Tree fuzzing clean; 4 vs 8 KiB decided; v2-vs-v3 benchmark (allocs, GC pause, RSS) | Planned |
| 4 | Concurrency: OLC, bounded retries, root compare-and-swap, EBR with lease | Concurrent stress with full-tree invariant check; 24-hour fuzz | Planned |
| 5 | Durability: WAL with group commit, copy-on-write checkpoints, recovery | 1,000 injected crashes recover consistently | Planned |

Beyond the storage engine, the long-term direction is etcd-shaped: MVCC revisions and Watch, then Raft replication and a gRPC API.

---

## Running the tests

Requires Go 1.22 or newer.

```bash
go test ./...                              # unit and integration tests
go test -race ./...                        # race detector (v2 reports expected OLC races, see P26)
go test -bench=. -benchmem ./...           # benchmarks (v2 numbers unreliable, see P39)
```

---

## Documentation

```
docs/
├── v2/
│   ├── architecture.md      how v2 works, as built
│   └── known-issues.md      44 issues: evidence, trigger, v3 resolution
└── v3/
    └── architecture-plan.md v3 design, page-size decision, roadmap, research
```

v1 is not documented yet.

---

## References

The v3 design draws on:

- Müller, Benson, Leis. *B-Trees Are Back: Engineering Fast and Pageable Node Layouts.* SIGMOD 2025.
- Leis, Haubenschild, Neumann. *Optimistic Lock Coupling.* IEEE Data Engineering Bulletin, 2019.
- Alhomssi, Leis. *Contention and Space Management in B-Trees.* CIDR 2021.
- Lehman, Yao. *Efficient Locking for Concurrent Operations on B-Trees.* ACM TODS, 1981.
- Boehm. *Can Seqlocks Get Along with Programming Language Memory Models?* MSPC 2012.
- VictoriaMetrics [fastcache](https://github.com/VictoriaMetrics/fastcache) for off-heap `mmap` allocation in Go.

The full reference list is in the v3 plan.
