# VelocaKV v3 Architecture Plan

Sep 29, 2026 · @Viraj Mandavkar

## Decision summary

v3 keeps the v2 slotted-page idea but rebuilds every page as 8 KiB of raw arena memory with a 64-byte header, 8-byte atomic slots carrying a 3-byte key head, stored fence keys, and restart-from-root OLC. This section supersedes the conflicting parts of the original v3 plan (4-byte slots, 24-byte header, 2 KiB inline value).

| Area | v2 | v3 decision | Fixes |
| --- | --- | --- | --- |
| Page size | 4 KiB, Go heap | **8 KiB**, mmap'd arena | GC thrash, P5 |
| Header | 32 B, sibling in Go pointer | **64 B** = one cache line, sibling as `SlotDescriptor` | P1, P2, P11 |
| Slot | 8 B: offset/suffix/val/ext | **8 B**: offset13 / keyLen13 / valLen12 / overflow / reserved / **head24** | P9, P23, P26, D4 |
| Delete | tombstone (`valLen = 0`) | **remove slot**, bytes become dead | P6, P20, P29 |
| Fences | none (last key used) | **low + high fence stored** in heap | P2, P28 |
| Prefix | set only in tests | **derived from fences**: `prefix = lowFence[:prefixLen]` | P13–P15, P18, D2 |
| Inline limit | none enforced | key+value ≤ **1,760 B**, else overflow chain | D1 |
| Compaction | only inside split/merge | **lazy, before split** | P22, D3 |
| Split point | slot median | **byte median, shortest separator, rightmost detection** | P29, D5–D7 |
| Merge | eager in delete path | **lazy (XMerge-style), same parent only, ≤ 75% fill** | P30–P33, D9 |
| Obsolete page | follow back-pointer | **restart from root** | P32, D8 |
| Root | `RWMutex` + pointer | **`atomic.Uint64` descriptor, CAS on split** | P35, P36, D10 |
| Internal nodes | Go slices + interfaces | **same slotted page format** | P34, GC pointers |
| EBR slots | `id % 128` | **per-worker handles, fail when full** | P38, D11 |
| API | allocates per call | **zero-alloc `Worker` API** + callback scan | P16, P17, P24 |

## Page size: 8 KiB

Decision: **8 KiB pages**, with `PageBits` kept a compile-time constant so both sizes can be benchmarked. The deciding factors are large etcd-style values and low write concurrency; 4 KiB wins only on contention and disk atomicity, and both are handled separately.

| Factor | 4 KiB | 8 KiB | Winner here |
| --- | --- | --- | --- |
| Inline record ceiling (≥ 4 records/page) | ≈ 750 B | **1,760 B** | 8 KiB: most etcd-style values (pod specs, configs) stay inline, no overflow hop |
| Records per page / fanout | 1× | \~2× | 8 KiB: shallower tree, fewer leaves per range scan |
| Binary search probes | log₂(n) | +1 probe | ≈ tie: heads make the extra probe a directory-only compare |
| Split / compact cost | 4 KiB memmove | 8 KiB memmove, half as often | tie |
| OLC false conflicts | 1 lock per \~4 KiB of keys | 2× more keys per lock | 4 KiB, unless writes are few (see assumption) |
| Prefix truncation | narrower range → longer fence prefix | wider range → slightly shorter prefix | 4 KiB, small margin |
| TLB | 1 OS page | 2 OS pages | tie once slabs use transparent huge pages |
| Offsets | 12 bits | 13 bits | tie: both fit the 13-bit slot field and uint16 header |
| Crash atomicity | often matches device atomic write unit | can tear on power loss | 4 KiB; 8 KiB needs CRC + copy-on-write checkpoints (planned anyway) |
| Arena bookkeeping | 16,384 pages / 64 MiB slab | 8,192 pages / slab | 8 KiB: half the free-list traffic |

**Assumption behind the call:** writes arrive through one apply loop (etcd/Raft style), so page-level write contention stays low while reads scale freely under OLC. If VelocaKV serves many concurrent writers on hot key ranges, add contention split (R9) first; if that is not enough, drop to 4 KiB.

**Revisit when:** median value < 200 B (time-series style), or benchmarks show validation-retry rates above \~1% on leaf pages.

### Making page size switchable

8 KiB is the default, not a proven result; the build must support both sizes so a benchmark decides. Page size is selected with a **build tag**, which keeps it a true constant and costs nothing at runtime.

| Approach | Hot-path cost | Verdict |
| --- | --- | --- |
| Build tag: `pagesize_8k.go` (default) and `pagesize_4k.go` (`//go:build page4k`), each defining `const PageBits` | Zero: every shift, mask and bounds check is constant-folded | **Used** |
| Runtime option stored in the file header | Every page-size calculation reads a variable; fixed-size page arrays become slices with extra bounds checks | Later, only if users need per-database sizes |
| Generics | Go type parameters take types, not constants; inlining not guaranteed | Rejected |

Rules that make the switch safe:

- **No literal `4096` or `8192` in the code.** Every size derives from `PageBits`:

```go
const (
    PageSize        = 1 << PageBits                          // 4096 | 8192
    HeaderSize      = 64
    Usable          = PageSize - HeaderSize                  // 4032 | 8128
    MaxFenceBytes   = 1024
    MaxInlineRecord = ((Usable-MaxFenceBytes)/4 - 8) &^ 31   // 736 | 1760
    PagesPerSlab    = SlabBytes >> PageBits                  // 16384 | 8192
)
```

- The 13-bit slot offset field and uint16 header offsets cover both sizes, so the slot and header formats do not change.
- Page size is written into the file header and fixed for the life of a database; opening a file with a mismatched build fails with `ErrPageSizeMismatch`.
- A lint test greps the source for `4096` and `8192` literals outside the two tag files and fails if it finds any.

### 4 KiB vs 8 KiB test plan

**Correctness first:** CI runs the full suite twice, `go test ./...` and `go test -tags page4k ./...`, including `FuzzPage`, `FuzzTree`, the torn-slot test and the concurrent stress test. A bug that appears under only one size means a hidden size assumption.

**Then the benchmark matrix.** Same machine, same `GOMAXPROCS`, trees closed after every run, each cell run 10 times and compared with `benchstat`.

| Axis | Values |
| --- | --- |
| Value size | 100 B, 1 KiB, 4 KiB (etcd pod-spec range) |
| Key set | etcd-style `/registry/<kind>/<ns>/<name>`, sequential, random, Zipfian (α = 0.99) |
| Workload | read-only Get, single-writer Put, multi-writer Put (GOMAXPROCS writers), 90/10 read/write mix, 100-key scans |
| Tree size | 100K, 1M, 10M keys |

| Metric | Why it matters for page size |
| --- | --- |
| ns/op, p99 latency | headline speed |
| OLC validation-retry rate | false conflicts grow with page size |
| Overflow-page reads per Get | 4 KiB pushes more values off-page |
| Records per leaf, tree height | fanout and depth |
| Split and compaction count | memmove work |
| RSS, bytes per record | space efficiency |
| allocs/op, GC pause total | must stay 0 and flat for both |

### Decision rule (fixed before running)

1. **Primary workload** = single-writer Put + read-only Get on etcd-style keys with 1 KiB values at 1M keys.
2. Keep **8 KiB** unless 4 KiB is more than **10% faster** on the primary workload, or uses more than 10% less RSS.
3. If 4 KiB wins only on multi-writer Put, add contention split (R9) and re-run before switching.
4. If the two sizes are within 10% everywhere, keep 8 KiB for the larger inline ceiling.
5. Record the result table and the chosen size in this doc; the losing size stays buildable for future re-tests.

## Page geometry

Every page is 8,192 bytes: a 64-byte header (exactly one cache line), then 8-byte slots growing forward, then free space, then the heap growing backward from byte 8192. Usable space is 8,128 bytes. An OLC reader's first load (the version word) pulls in every header field it needs in the same line.

```
0          64                                   heapStart                  8192
┌──────────┬─────────────────┬────── FREE ──────┬────────────────────────────┐
│ Header   │ Slots →         │                  │ ← records   │ fences       │
│ 64 B     │ 8 B × slotCount │                  │ (key|value) │ (low, high)  │
└──────────┴─────────────────┴──────────────────┴────────────────────────────┘
freeSpace = heapStart − (64 + 8 × slotCount)      (derived, never stored)
```

### Header layout (all little-endian)

| Offset | Size | Field | Notes |
| --- | --- | --- | --- |
| 0 | 8 | `version` | bit 0 lock, bit 1 obsolete, bits 2–63 counter, step 4 (unchanged from v2) |
| 8 | 4 | `gen` | bumped each time the page leaves the free list; ABA guard for `SlotDescriptor` |
| 12 | 1 | `kind` | 0 leaf, 1 inner, 2 overflow |
| 13 | 1 | `flags` | bit 0 has overflow records, bit 1 last insert was at the end (rightmost-split hint) |
| 14 | 2 | `slotCount` |  |
| 16 | 8 | `link / crc` | on free list: next descriptor; live: CRC32 written at checkpoint only |
| 24 | 8 | `rightSib` | `SlotDescriptor` (pageID u32, gen u32); 0 = none |
| 32 | 8 | `upperChild` | inner pages only: rightmost child descriptor |
| 40 | 2 | `heapStart` | lowest occupied heap byte; **stored**, so the heap boundary is never inferred (P11) |
| 42 | 2 | `deadBytes` |  |
| 44 | 2 | `lowFenceOff` |  |
| 46 | 2 | `lowFenceLen` | 0 = −∞ |
| 48 | 2 | `highFenceOff` |  |
| 50 | 2 | `highFenceLen` | `0xFFFF` = +∞ |
| 52 | 2 | `prefixLen` | prefix = `lowFence[:prefixLen]`; no separate prefix bytes |
| 54 | 10 | reserved | scan counter, hint-array offset, future MVCC |

### Invariants (checked by `page.Check()` in debug builds)

1. `64 + 8·slotCount ≤ heapStart ≤ 8192`
2. Every record lies inside `[heapStart, 8192)` and never overlaps the fences.
3. `8128 = freeSpace + 8·slotCount + fenceBytes + liveBytes + deadBytes`
4. Slots are strictly sorted by full key; every key k satisfies `lowFence ≤ k < highFence`.
5. `prefixLen` = common prefix length of the two fences.
6. The version word is 8-byte aligned (asserted once at arena init).

## Slot format

Each slot is one 8-byte word, read with a single `atomic.LoadUint64` and written with a single `atomic.StoreUint64`. It carries a 3-byte key head so most binary-search probes never touch the heap. This replaces the original plan's 4-byte slot, which had no room for a head and needed two fields per record.

```
bit 63            51 50            38 37          26  25   24  23                     0
┌──────────────────┬──────────────────┬──────────────┬────┬────┬────────────────────────┐
│ offset (13)      │ keyLen (13)      │ valLen (12)  │ OVF│ RSV│ head (24)              │
│ heap byte 0–8191 │ suffix bytes     │ 0–4095       │    │    │ first 3 suffix bytes,  │
│                  │                  │              │    │    │ big-endian, 0-padded   │
└──────────────────┴──────────────────┴──────────────┴────┴────┴────────────────────────┘
```

- **Head at bit 0:** the hottest field extracts with one AND (`slot & 0xFFFFFF`), the Phase 2 rule.
- **Big-endian head:** integer compare of two heads gives the same order as comparing the bytes.
- **Tie → heap:** equal heads (or keys shorter than 3 bytes) fall through to a full compare, so padding can never produce a wrong answer.
- **OVF bit:** value lives in an overflow chain; the inline value area is 8 bytes (`headPageID u32, totalLen u32`).
- **RSV bit:** held for MVCC/tombstones later. Deletes do not use it (see Mutations).
- **No external/arena-handle flag:** the v2 `isExt` path is removed (P7, P8).

### Why 8 bytes and 3-byte heads, not the paper's 10-byte slot with 4-byte heads

The SIGMOD'25 layout packs offset, key length and value length (16 bits each) plus a 4-byte head into 10 bytes and reads it unaligned. That works in C++. In Go, OLC readers must load slots atomically to stay inside the memory model (R13), and `atomic.LoadUint64` requires 8-byte alignment. An 8-byte slot is the widest word that is atomic, aligned, and packs 8 per cache line. The cost is one head byte; the benchmark ablation (heads on/off) decides whether that byte matters.

### Search cost with heads (200 live records, cold page)

| Touch | v2 | v3 |
| --- | --- | --- |
| Header line | 1 | 1 (prefix comes from the low fence, often on a line already loaded) |
| Directory lines | \~5 | \~5 |
| Heap lines | \~8 | \~1–2 (ties only) |
| **Total** | **\~15** | **\~7–8** |

## Records, inline ceiling, overflow

A record is `keySuffix ‖ value` contiguous in the heap, with no length bytes (lengths live in the slot). Records whose suffix + value exceed **1,760 bytes** move the value to an overflow chain, so every page is guaranteed to hold at least 4 records and every split can succeed.

### Limits

| Limit | Value | Derivation |
| --- | --- | --- |
| `MaxKeyLen` (full key) | 512 B | caps fence size and keeps pivots small |
| Fence budget | ≤ 1,024 B | two fences ≤ 512 B each; usually far shorter (truncated separators) |
| `MaxInlineRecord` (suffix + value) | 1,760 B | (8,128 − 1,024) ÷ 4 − 8 = 1,768 → rounded down to 32 B |
| Max value | 1.5 MiB (per v3 plan) | ≈ 194 overflow pages |

The original plan's 2,048 B inline *value* breaks the 4-records guarantee once key and slot are counted: 4 × (2,048 + key + 8) > 8,128. It is superseded.

### Overflow pages (`kind = 2`)

- Layout: 64 B header + 8,128 B of raw value bytes; `link` field chains to the next page.
- The leaf keeps `suffix ‖ headPageID u32 ‖ totalLen u32` with OVF set in the slot.
- **Updates never modify a live chain.** A new value gets a new chain; the old chain is retired through EBR in one step. Readers holding the old descriptor keep a consistent value.
- Readers validate the leaf version **after** copying the whole chain; each chain page's `gen` is checked against the descriptor.

## Mutations and compaction

Deletes now **remove the slot** instead of leaving a tombstone. v2 kept tombstone slots so concurrent readers' indexes stayed stable; under OLC every reader validates anyway, so that reason is gone. Removing the slot deletes P6, P20 and P29 outright and drops the resurrection path.

### Operations (all under the leaf's write lock)

| Op | Heap | Slots | Accounting |
| --- | --- | --- | --- |
| Insert | carve `payload` at `heapStart − payload` | shift right, write new slot last | `heapStart −= payload` |
| Update, same or smaller | overwrite value in place | rewrite slot (valLen, head unchanged) | `deadBytes += slack` |
| Update, larger | write full record at new heap front | one atomic slot store swings offset | `deadBytes += oldPayload` |
| Delete | nothing moves | shift left | `deadBytes += payload` |

Write order stays: payload bytes first, slot store last. Slots and header fields are written with atomic stores; payload bytes are plain copies protected by version validation.

### Lazy compaction (insert/update path)

1. `need = payload + 8` (insert) or `payload` (grow).
2. `freeSpace ≥ need` → write.
3. Else `freeSpace + deadBytes ≥ need` → **compact, then write.**
4. Else split.

Compaction copies fences first (top of heap), then live records in slot order into the worker's **scratch page** (an arena page owned by the worker, reused forever: zero allocation). It then copies bytes `[8:8192)` back, never the version word, and unlocks (version += 4). `heapStart` and `deadBytes` are recomputed from the cursors, which wipes any accounting drift.

### No silent drops

Every mutation returns an error or success. A path that cannot place a record (should be impossible given the 4-records guarantee) returns `ErrPageInvariant` and trips a debug assertion, never a silent `false` (P19, P22).

## Split, merge, separators

Splits pick a short separator near the **byte** median and set real fences on both halves; merges are lazy, same-parent only, and target ≤ 75% fill. The fence keys make move-right decisions exact and let each half's prefix grow automatically.

### Separator selection

1. Find the slot where cumulative live bytes cross half the page (byte median, not slot median).
2. Take a window of `max(2, count/16)` slots around it.
3. In that window, pick the adjacent pair whose keys diverge earliest; separator = shortest byte string with `left < sep ≤ right` (common prefix + 1 byte).
4. **Rightmost hint:** if `flags.lastInsertAtEnd` is set and the new key is ≥ every key on the page, split near the end instead (left keeps \~90%). Sequential loads then fill leaves instead of leaving them half empty.

### Split (B-link half-split, leaf lock held)

1. `Alloc()` right page (private, unreachable).
2. `right.low = sep`, `right.high = left.high`; copy records ≥ sep, re-truncated to the right page's new prefix.
3. `right.rightSib = left.rightSib` **before** publishing.
4. Rebuild left in the scratch page with `left.high = sep` (its prefix may lengthen), copy back.
5. `atomic.StoreUint64(&left.rightSib, right)`; unlock left (version += 4).
6. Insert `(sep → right)` into the parent under the parent's lock. Parent obsolete or `gen` mismatch → restart from root.

Between steps 5 and 6, a reader for key k with `k ≥ left.high` follows `rightSib`. The rule compares against the **stored fence**, never the last key on the page (P28).

### Merge (lazy)

- Runs from a background sweep or when the allocator nears `MaxArenaBytes`, on randomly chosen leaves.
- Candidate: left and right siblings **under the same parent** (P33), combined live bytes after re-truncation ≤ 6,096 B (75% of 8,128).
- Lock order: parent → left → right. Split never holds a leaf lock while taking a parent lock, so there is no cycle.
- Every lock call's result is checked; an obsolete page aborts the merge (P30).
- Dry run (prefix, size) touches nothing; then copy right into left, set `left.high = right.high`, `left.rightSib = right.rightSib`, drop the separator from the parent, `MarkObsolete(right)`, retire right via EBR.
- Readers stranded on `right` see obsolete and **restart from the root** (P32). No back-pointer.

## Concurrency: OLC protocol

Readers never write shared memory, never trust an offset before bounds-checking it, validate every answer including "not found", and restart from the root on any obsolete or recycled page. After 16 failed attempts a reader takes the page's write lock, the forward-progress fix from the original OLC work.

### Read (one page)

1. Resolve the parent's descriptor → page. `page.gen ≠ desc.gen` → **restart from root**.
2. `v = ReadLockOrSpin()`. Obsolete → **restart from root**.
3. Load header `words and slots with atomic.LoadUint64 (Go has no 16-bit atomics, so 2-byte fields are extracted from their aligned 8-byte header word)`.
4. **Bounds-check before use:** `64 + 8·slotCount ≤ heapStart`, `heapStart ≤ offset`, `offset + keyLen + valLen ≤ 8192`, `keyLen ≤ 512`. Any failure = treat as a failed validation and retry. (In a raw arena a bad offset reads a *neighbouring page* silently instead of panicking: P23 gets worse, not better.)
5. Copy the answer (value, or "child descriptor", or "not found") into worker-owned memory.
6. `Validate(v)`. Fail → retry; attempt 16 → take the write lock for this read.

### Write (leaf)

1. Descend optimistically (read protocol on inner pages, no locks).
2. `WriteLock(leaf)`; check `gen`; check `low ≤ key < high`. Key ≥ high → unlock, follow `rightSib`. Key < low or page obsolete → unlock, restart from root.
3. Mutate (payload first, atomic slot store last), unlock (version += 4).

### Go memory model

Page payload bytes are copied with plain loads while a writer may be copying with plain stores. Go treats that as a data race: an implementation may report it and terminate. Two rules keep this sound:

- Everything a reader **branches on** (version, header fields, slots, descriptors) is loaded atomically. Plain loads touch only the bytes being copied out, which are discarded unless validation passes.
- `//go:build race` builds make readers take the write lock every time, so `go test -race` checks every other access without drowning in expected OLC races. Production builds stay optimistic.

### Contention

Page-level versions mean a write anywhere on a page fails every concurrent reader on it (false conflicts). Accepted under the single-writer assumption. If retry metrics climb, add **contention split** (R9) before touching page size.

## Tree layer

Inner nodes become ordinary slotted pages (`kind = 1`), so the whole tree lives in the arena and the Go GC sees no tree pointers at all. The root is one atomic 64-bit descriptor updated by CAS.

### Inner pages

- Record = `separatorSuffix ‖ childDescriptor (8 B)`; the rightmost child sits in the header's `upperChild`.
- Same heads, fences, prefix truncation and OLC rules as leaves.
- Routing: binary search (heads first) for the first separator > key → that record's child; none → `upperChild`. A key equal to a separator goes right, matching leaf splits.
- Inner splits **promote** the middle separator (removed from both halves); leaf splits **copy** it (the record stays in the right leaf).
- Removes P34 (torn slice headers) and the \~130 GC-traced pointers per v2 inner node.

### Root

- `root atomic.Uint64` holds the root descriptor. Readers load it once per operation: no mutex (P35).
- Root split: the splitting worker holds the old root's lock, builds a new inner page `[sep] → (oldRoot, right)`, then `CompareAndSwap(oldRootDesc, newRootDesc)`. CAS failure means someone else changed the root: unlock, retire the unused page, restart from root (P36).
- Height only grows at the top; all leaves stay at the same depth.

### SlotDescriptor

```
63             32 31              0
┌────────────────┬─────────────────┐
│ gen (u32)      │ pageID (u32)    │
└────────────────┴─────────────────┘
```

Any reader holding a descriptor whose `gen` no longer matches the page header knows the page was recycled and restarts from the root. EBR prevents that case in normal operation; `gen` is the second line of defense.

## Arena, allocator, EBR

Slabs come from anonymous `mmap`, not `make([]byte)`: the memory stays outside the Go heap entirely, so it neither gets scanned nor inflates the GC's heap target, and every slab starts OS-page aligned. This is the same technique VictoriaMetrics uses in fastcache ([source](https://github.com/VictoriaMetrics/fastcache/blob/master/README.md)).

### Arena

- Slab = 64 MiB = 8,192 pages. `pageID u32` → `slab = id >> 13`, `addr = slabs[slab].base + (id & 8191) << 13`.
- Slab table = atomic pointer to an immutable slice; growth publishes a new slice (readers never lock to resolve).
- `madvise(MADV_HUGEPAGE)` on slabs where available: 2 MiB TLB entries make the 4 vs 8 KiB TLB question moot.
- Init asserts: base 4096-aligned, `unsafe.Sizeof` checks, header word offsets 8-aligned.
- `MaxArenaBytes` enforced in slab growth → `ErrArenaFull`, never an OOM kill.

### Allocator

- Sharded Treiber free lists (per the original plan), each head padded to 64 B.
- Intrusive link in header bytes `[16:24]`; head word = (pageID, pop counter) to stop ABA on the stack itself.
- `Alloc()` bumps `gen`; pre-faulter keeps ≥ 256 pages (2 MiB) committed ahead of demand.

### Page lifecycle

```
free list ──Alloc (gen++)──► live ──unlink (merge / overflow replace / root race)──► retired (EBR bin)
    ▲                                                                                       │
    └──────────────────────────── two epoch advances later ─────────────────────────────────┘
```

### EBR

- Workers register once: `db.NewWorker()` takes a slot from a free list sized `4 × GOMAXPROCS`; none left → `ErrTooManyWorkers` (P38). `Worker.Close()` returns it.
- Each op: `Enter` (store global epoch, re-check) … `Exit` (store inactive). Slots padded to 64 B.
- `Retire(page)` → bin `epoch % 3`; advancer every 10–50 ms frees the bin from two epochs back.
- 5-second reader lease: a worker stuck past it is marked expired, its next op returns `ErrSnapshotExpired`, and reclamation continues.
- Why EBR over alternatives: lowest per-read cost for a read-heavy tree; its known weakness (one stalled thread blocks reclamation) is exactly what the lease covers. Hazard pointers pay a fence per pointer followed; QSBR blocks just like EBR without the per-op marker (see R14).

## Zero-alloc API and scan

Every hot-path call reuses memory owned by a `Worker` (EBR slot, scratch page, key buffer) or supplied by the caller. Target: **0 B/op, 0 allocs/op** for Get, Put, Delete and Scan in steady state.

```go
type Worker struct {
    slot    int      // EBR slot
    scratch uint32   // pageID of a private arena page
    keyBuf  [512]byte
    rows    []byte   // reused scan batch buffer
}

func (db *DB) NewWorker() (*Worker, error)
func (w *Worker) Close()

func (w *Worker) Get(key, dst []byte) ([]byte, error)          // appends value to dst
func (w *Worker) Put(key, val []byte) error
func (w *Worker) Delete(key []byte) (bool, error)
func (w *Worker) Scan(start, end []byte, fn func(k, v []byte) bool) error
```

### Scan, per leaf

1. Descend once with `start`; on each leaf take `v`.
2. `SeekSlot(start)` with heads, not a walk from slot 0 (P24).
3. Copy matching records (prefix + suffix assembled straight into `w.rows`, value after it) until `end` or the leaf ends.
4. `Validate(v)`. Fail → re-seek from the last key already emitted (exclusive), same leaf or from root.
5. Pass → call `fn` for each copied row. Slices are valid only during the callback.
6. Next leaf via `rightSib`, with the same read protocol. Obsolete → restart from root at the last emitted key (P32, P37).

One descent, then sideways; every leaf's rows are validated before the caller sees them, and no key is ever allocated (P16, P17).

## Durability notes

The WAL, group commit, checkpointing and mark-and-sweep free-list recovery from the original plan stand unchanged. The page decisions above add three requirements.

1. **Torn 8 KiB pages.** An 8 KiB page can be half-written on power loss. Checkpoints go to a new file (or new region), are `fdatasync`ed, then atomically switched in; a torn page can only ever be in the checkpoint being written, never the last good one. Recovery = last good checkpoint + WAL replay.
2. **CRC only at checkpoint.** The CRC32 in header `[16:20]` is computed when a page is flushed and verified on load, never on every mutation (that would put a 8 KiB checksum on the write path).
3. **Descriptors are file offsets.** `pageID` doubles as the page's position in the checkpoint file, so inner pages need no pointer rewriting on load. `gen` values are persisted and bumped on reuse after recovery like any other allocation.

## Testing strategy

The invariant checker is the foundation: every page operation in debug builds ends with `page.Check()`, so an accounting bug fails at the op that caused it, not 10,000 ops later.

| Layer | Test | Catches |
| --- | --- | --- |
| Page | `page.Check()` after every op (debug build) | P11, P18, P21-class accounting drift |
| Page | `FuzzPage`: random insert/update/delete/compact vs a Go `map` model | ordering, bounds, lost records |
| Page | Torn-slot test: feed random 8-byte slot words to the reader path | P23 (must retry, never read out of bounds) |
| Tree | `FuzzTree`: random op sequences + splits/merges vs model | P19, P22, P28, P29 |
| Tree | Concurrent stress: writers + readers + scanners, then full-tree `Check()` | P25, P32, P36, P37 |
| Tree | `-race` build (pessimistic readers) | every non-OLC race |
| Arena | 5M concurrent Alloc/Free, 0 allocs/op (original plan) | allocator ABA |
| EBR | Stalled-worker test: lease expiry frees bins | leak |
| Crash | VFS fault injection (original plan) + torn-page injection | checkpoint atomicity |

### Benchmarks (quote only these numbers)

- Every benchmark closes its tree (P39). Report ns/op, allocs/op, B/op, p99 latency, GC pause total (`runtime/metrics`), RSS.
- Key sets: etcd-style (`/registry/<kind>/<ns>/<name>`), sequential, random, Zipfian (α = 0.99).
- Ablations: heads on/off, 4 vs 8 KiB (`build tag page4k; full matrix and decision rule in Page size`), lazy vs eager compaction.
- Always against the v2 baseline, same machine, same `GOMAXPROCS`.

## Roadmap and priorities

Build in five phases, each gated by a test that must pass before the next starts. For a 30-day VictoriaMetrics application, phases 1–2 plus a single-threaded tree and the v2-vs-v3 benchmark are the demo; concurrency and durability are the "here's what's next" story.

1. **Arena + allocator** (original Phase 1, with `mmap` slabs, alignment asserts, per-worker registration). Gate: 5M concurrent Alloc/Free at 0 allocs/op; `-race` clean.
2. **Page format** — header, 8-byte slots with heads, fences, fence-derived prefix, overflow pages, lazy compaction, `page.Check()`. Gate: `FuzzPage` 1 hour clean under both page4k and default builds; torn-slot test never reads out of bounds; no 4096/8192 literals outside the tag files.
3. **Single-threaded tree** — inner pages, splits with separator selection and rightmost hint, lazy merge, zero-alloc `Worker` API, scan. Gate: `FuzzTree` clean; 4 KiB vs 8 KiB matrix run and page size decided; **benchmark vs v2: 0 allocs/op, GC pause reduction, RSS**. ← 30-day demo line
4. **Concurrency** — OLC protocol, bounded retries, root CAS, EBR with lease, `-race` pessimistic build. Gate: concurrent stress + full-tree `Check()`; 24-hour fuzz (original plan).
5. **Durability + tooling** — WAL, copy-on-write checkpoints, recovery, doctor CLI, metrics (original Phases 4–5). Gate: 1,000 injected crashes recover consistently.

### Tier list (from the parking-lot triage)

- **Tier 1, correctness:** tombstone removal (P6/P20), fences (P2/P28), bounds-checked atomic reads (P23/P26), validated not-found (P25), OLC scan (P37), no silent drops (P19/P22), same-parent merge + lock checks (P30/P33), restart from root (P32), root CAS (P35/P36), worker registration (P38).
- **Tier 2, performance:** heads, fence-derived prefix, separator selection, lazy compaction, zero-alloc scan, `mmap` arena.
- **Tier 3, next:** contention split, dense leaves for timestamp keys, adaptive leaf layout, hint array.

## Appendix A: v2 parking lot (P1–P39)

21 real bugs (4 data-loss or crash class: P19, P22, P23, P33), 17 design debts, 1 non-issue. "v3 status" points to the section that resolves each.

| # | Item | Verdict | v3 status |
| --- | --- | --- | --- |
| P1 | Header `[8:16]` sibling bytes unused; real sibling is a Go pointer | Debt | Fixed: `rightSib` descriptor |
| P2 | HighKey fields reserved, never written | Debt → causes P28 | Fixed: stored fences |
| P3 | Version word via `unsafe` on `data[0:8]`, aligned by accident | Debt | Fixed: init alignment asserts |
| P4 | No page constructor | Debt | `initPage(kind, low, high)` |
| P5 | 4 KiB TLB benefit theoretical on Go heap | Debt | Fixed: `mmap` + huge pages |
| P6 | `valLen == 0` tombstone; empty value = deleted | **Bug** | Fixed: slot removal on delete |
| P7 | `valLen` means two things for external values | Debt | Fixed: overflow record |
| P8 | `isExt` dead; Scan would return handle bytes | **Bug** (latent) | Removed; overflow path tested |
| P9 | 31-bit offset, 19 wasted bits | Debt | Fixed: bits reused for head |
| P10 | Offset widths differ header vs slot | Debt | Fixed: 13-bit everywhere |
| P11 | Heap boundary derived, never asserted | Debt | Fixed: `heapStart` stored + `Check()` |
| P12 | Redundant `maxAllowedPayload` check | Not a flaw | — |
| P13 | Prefix never set in tree path | Debt | Fixed: fence-derived prefix |
| P14 | `deltaPrefixLen` always 0 | Debt | Obsolete |
| P15 | Split never lengthens prefix | Debt | Fixed: new fences per half |
| P16 | `AssembleFullKey(nil)` allocates per call | Debt (perf) | Fixed: worker buffers |
| P17 | Scan allocates each key twice | Debt (perf) | Fixed: batch buffer |
| P18 | Stranded prefix bytes on empty-page reset | **Bug** | Fixed: prefix lives in fences |
| P19 | `insertIntoLeaf` false ignored → silent drop | **Bug, data loss** | Fixed: errors, no drops |
| P20 | Empty-value update = accidental tombstone, wrong accounting | **Bug** | Fixed with P6 |
| P21 | EvaluateCapacity double-counts slots | **Bug** (perf) | Fixed: single capacity rule |
| P22 | No compaction trigger; 1-record page drops updates | **Bug, data loss** | Fixed: lazy compaction |
| P23 | Torn slot → out-of-range offset → panic | **Bug, crash** | Fixed: bounds check before use |
| P24 | Scan walks from slot 0 | Debt (perf) | Fixed: seek |
| P25 | Not-found returned without Validate | **Bug** | Fixed: validate every answer |
| P26 | Plain page reads race with plain writes | **Bug** (formal) | Fixed: atomic control reads, race build |
| P27 | MarkObsolete CAS implies unlocked safety | Debt | Assert lock held |
| P28 | Fence = last key; shrinks after delete | **Bug** | Fixed: stored fences |
| P29 | Split midpoint counts tombstones → empty left | **Bug** | Fixed: no tombstones, byte median |
| P30 | `right.WriteLock()` result ignored in merge | **Bug** | Fixed: checked |
| P31 | Merge dry-run uses wrong suffix length | **Bug** (latent) | Fixed: re-truncation in dry run |
| P32 | Scan follows dead page back-pointer → duplicates | **Bug** | Fixed: restart from root |
| P33 | Merge across parents → cousin points at retired page | **Bug, UAF in v3** | Fixed: same-parent only |
| P34 | Inner slice header swap non-atomic | **Bug** | Fixed: inner pages in arena |
| P35 | Every op takes `t.mu.RLock()` | Debt (perf) | Fixed: atomic root |
| P36 | Concurrent root splits stack roots | **Bug** | Fixed: root CAS |
| P37 | Scan does no OLC on leaves | **Bug** | Fixed: validated batches |
| P38 | EBR slot wraps at 128 | **Bug** (fatal in v3) | Fixed: registration + error |
| P39 | Benchmarks leak trees (no `Close()`) | **Bug** (test) | Fixed: benchmark rules |

## Appendix B: design ideas (D1–D11)

All eleven ideas from the masterclass hold up against the literature; every one is adopted in this plan.

| # | Idea | Research check | Where in plan |
| --- | --- | --- | --- |
| D1 | Geometry-derived inline ceiling, not data-driven | Standard practice (SQLite, Postgres TOAST, InnoDB) | Records: 1,760 B |
| D2 | Prefix from fence keys | Müller et al.: prefix = common prefix of fences, changes only on split/merge; URL keys went from 36 to 96 records/leaf | Geometry, Split |
| D3 | Compact before splitting | Same paper compacts lazily during insertion | Mutations |
| D4 | Key heads in slot | +16–64% lookup/insert, \~5.7 B/record | Slot format (3-byte head) |
| D5 | Split by bytes, not slot count | Separator chosen in a count/16 window around the median | Split |
| D6 | Rightmost split for sequential inserts | Ordered inserts give low fill with median splits | Split (rightmost hint) |
| D7 | Shortest separators | Truncate past the first differing byte | Split |
| D8 | Obsolete page → restart from root | Matches original OLC `writeUnlockObsolete` design | Concurrency |
| D9 | Merge with real hysteresis | Paper merges at ≥ 3/4 unused; plan uses ≤ 75% combined fill | Merge |
| D10 | Atomic root pointer | Removes the RWMutex cache-line bouncing | Tree layer |
| D11 | EBR slots per worker, fail when full | Follows from P38 | Arena, EBR |

## Appendix C: research-backed ideas

Numbers below come from the cited papers' own benchmarks (C++, not Go); treat them as direction, and confirm each with a VelocaKV ablation before claiming it.

| # | Technique | Measured effect | Status in v3 |
| --- | --- | --- | --- |
| R1 | Fence-based prefix truncation | 7–64% less space; biggest on URL-like keys | Adopted |
| R2 | Key heads (4 B in paper, 3 B here) | +16–64% lookups/inserts; +3–12% scans | Adopted |
| R3 | Hint array (16 sampled heads, 64 B) | +25% integer lookups, ≈0 for strings; 1.5–2% space | Tier 3 |
| R4 | Fingerprinting leaves (1-byte hashes, unsorted) | +13–22% string lookups; worse scans | Not adopted (scan-heavy etcd ranges) |
| R5 | Dense leaves (array by key offset) | up to +71% lookup, +213% insert, −52% space at 100% density | Tier 3: timestamp/ID keys |
| R6 | Adaptive leaf layout chosen at split/merge | ≥ 98% of best fixed layout in almost all cases | Tier 3 |
| R7 | Merge only when ≥ 3/4 unused | avoids split/merge thrash | Adopted (as ≤ 75% fill) |
| R8 | Lazy compaction in insert | fewer splits | Adopted |
| R9 | Contention split (split leaves hit by ≥ 1/30 contended writes) | keeps write throughput flat as page size grows | Tier 3, first lever if retries climb |
| R10 | XMerge (lazy merge of random underfull nodes) | compact tree regardless of insert order | Adopted |
| R11 | Bounded OLC restarts → write lock | guarantees reader progress | Adopted (16) |
| R12 | Obsolete marking + restart | standard OLC | Adopted |
| R13 | Seqlock data must be atomic (Boehm; Abseil `SequenceLock`) | memory-model soundness | Adopted for control data + race build |
| R14 | EBR vs QSBR vs hazard pointers vs IBR | EBR fastest, unbounded under stalls | EBR + lease |
| R15 | Off-heap `mmap` chunks (VictoriaMetrics fastcache) | \~1M pointers for 64 GB instead of \~1B | Adopted |
| R16 | Blink-hash hash leaves for monotonic timestamps | up to 91.3× ingestion on time-series inserts | Tier 3, interview topic |
| R17 | OptiQL queue-based optimistic lock | avoids collapse under high contention | Watch |

R4 is the correct form of the "store hashes in the slot" idea from the masterclass: hashes work for point lookups in **unsorted** leaves, never inside a binary search.

## Appendix D: interview stories

Each story is a real v2 failure, the mechanism, and the v3 decision it forced. Lead with the number.

1. **"My scan path allocated \~300M objects per second."** Three allocations per result × 100 results × 1M scans/s. → Worker-owned buffers, zero-alloc API, arena.
2. **"A single hot key could silently lose updates."** Growing values burn heap quadratically; with no compaction trigger, a one-record page could neither compact nor split. → Lazy compaction, errors instead of silent `false`.
3. **"The dead page's back-pointer fixed Get and broke Scan."** Pointing back gave Scan duplicates; pointing forward gave Get false misses. → Restart from root.
4. **"In a raw arena a torn offset doesn't panic; it reads a neighbour page."** Go's bounds checks were hiding a correctness bug. → Bounds-check every offset before validation.
5. **"I gave up one head byte to keep slots atomic."** The paper's 10-byte slot is faster in C++, but Go's 64-bit atomics need alignment. → 8-byte slot, 3-byte head, measured by ablation.
6. **"8 KiB because the values are big and writes are serialized."** The inline ceiling doubles; contention and torn pages are handled by contention split and copy-on-write checkpoints.

## References

- Müller, Benson, Leis. [B-Trees Are Back: Engineering Fast and Pageable Node Layouts](https://www.cs.cit.tum.de/fileadmin/w00cfj/dis/papers/btrees-are-back.pdf), SIGMOD 2025.
- Leis, Scheibner, Kemper, Neumann. [The ART of Practical Synchronization](https://db.in.tum.de/~leis/papers/artsync.pdf), DaMoN 2016.
- Leis, Haubenschild, Neumann. [Optimistic Lock Coupling](http://sites.computer.org/debull/A19mar/p73.pdf), IEEE Data Eng. Bull. 2019.
- Alhomssi, Leis. [Contention and Space Management in B-Trees](https://www.cidrdb.org/cidr2021/papers/cidr2021_paper21.pdf), CIDR 2021.
- Boehm. [Can Seqlocks Get Along with Programming Language Memory Models?](https://www.hpl.hp.com/techreports/2012/HPL-2012-68.html), MSPC 2012.
- Abseil. [SequenceLock](https://android.googlesource.com/platform/external/abseil-cpp/+/0740be76f23ee9f9d42f4a08552ec4722a6d8da6/absl/flags/internal/sequence_lock.h) (atomic-word seqlock data).
- [The Go Memory Model](https://go.dev/ref/mem).
- VictoriaMetrics. [fastcache README](https://github.com/VictoriaMetrics/fastcache/blob/master/README.md) and [malloc\_mmap.go](https://github.com/VictoriaMetrics/fastcache/blob/master/malloc_mmap.go).
- Cha et al. [Blink-hash: An Adaptive Hybrid Index for In-Memory Time-Series Databases](https://www.vldb.org/pvldb/vol16/p1235-cha.pdf), VLDB 2023.
- Wen et al. [Interval-Based Memory Reclamation](https://www.cs.rochester.edu/u/scott/papers/2018_PPoPP_IBR.pdf), PPoPP 2018.
- Brown. [Reclaiming Memory for Lock-Free Data Structures](https://arxiv.org/pdf/1712.01044).
- [OptiQL: Robust Optimistic Locking for Memory-Optimized Indexes](https://dl.acm.org/doi/abs/10.1145/3617336), SIGMOD 2024.
