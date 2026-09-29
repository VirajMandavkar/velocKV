# VelocaKV v2 Architecture

> **Status:** superseded by v3. This document describes v2 **as built**, not as intended.
> Where a mechanism exists in the code but is not used, or behaves differently from its design, an **As built** note says so and links the issue in [`v2-known-issues.md`](./v2-known-issues.md). Issue details live there; this document does not repeat them.

---

<a id="s1"></a>
## 1. What v2 is

v2 is an in-memory, concurrent, ordered key-value index in pure Go. It stores variable-length keys and values in fixed-size **4 KiB slotted pages**, links the pages into a **B-link tree**, and lets readers run without locks using **Optimistic Lock Coupling (OLC)**. An **Epoch-Based Reclamation (EBR)** manager exists as scaffolding for safe memory reuse.

v2 has no durability: no write-ahead log, no checkpoints, no disk format. Everything lives on the Go heap.

> **As built:** EBR tracks epochs and retires merged pages, but the reclamation step discards its result; the Go garbage collector does all freeing (section 17).

### Component map

```
                        ┌─────────────────────────────┐
   Get / Put /          │ Tree                        │
   Delete / Scan  ────► │  root pointer (RWMutex)     │
                        │  EBR manager                │
                        │  reclamation goroutine      │
                        └──────────────┬──────────────┘
                                       │ root
                                       ▼
                        ┌─────────────────────────────┐
                        │ Internal node               │   Go structs:
                        │  separator keys             │   slices of keys,
                        │  child pointers             │   slices of children
                        └───┬───────────┬─────────┬───┘
                            ▼           ▼         ▼
                        ┌───────┐   ┌───────┐   ┌───────┐
                        │ Leaf  │──►│ Leaf  │──►│ Leaf  │     4 KiB slotted pages,
                        │ page  │   │ page  │   │ page  │     linked left → right
                        └───────┘   └───────┘   └───────┘
```

| Component | Source file | Role |
|---|---|---|
| Page layout constants | `page_layout.go` | Sizes, header offsets, version bits, slot masks |
| Slotted page | `slotted_page.go` | Records, search, mutations, compaction, split, merge, page-level OLC |
| Tree | `tree.go` | Internal nodes, routing, split/merge propagation, root, scan |
| EBR | `ebr.go` | Epochs, per-thread slots, retire bins |

---

<a id="s2"></a>
## 2. The page: 4,096 bytes

A page is a flat array of 4,096 bytes plus one Go pointer to its right sibling that lives **outside** the array.

```
Byte 0      32                                                             4096
┌──────────┬────────────────────┬─────────────────────┬─────────────────────┐
│  Header  │  Slot directory →  │     FREE SPACE      │  ← Heap (records)   │
│  32 B    │  8 B per slot      │     (one gap)       │  + shared prefix    │
└──────────┴────────────────────┴─────────────────────┴─────────────────────┘
           ▲                    ▲                     ▲                     ▲
           32          32 + 8 × slotCount        heap bottom              4096
```

- **Header:** page metadata and the concurrency word.
- **Slot directory:** sorted array of fixed 8-byte descriptors, one per record, growing right.
- **Heap:** the key and value bytes, growing left from the end of the page.
- **Free space:** the single gap between them.

Usable space after the header: **4,064 bytes**.

The 4 KiB size was chosen to match the operating-system page size (one TLB entry, one disk block).

> **As built:** there is no page constructor; the same initialisation sequence is repeated in the tree, in split, and in tests ([P4](./v2-known-issues.md#p4)).

> **As built:** the page object is 4,104 bytes (array + sibling pointer), which the Go allocator rounds up to a 4,864-byte size class, and it is not aligned to a 4 KiB boundary. The TLB and alignment benefits do not materialise. See [P5](./v2-known-issues.md#p5), [P40](./v2-known-issues.md#p40).

---

<a id="s3"></a>
## 3. The header: 32 bytes

All multi-byte fields are little-endian and naturally aligned.

| Offset | Size | Field | As built |
|---|---|---|---|
| 0–7 | 8 | Version word | Used (section 4) |
| 8–15 | 8 | Right sibling | **Reserved, never read or written**; the sibling is a Go pointer ([P1](./v2-known-issues.md#p1)) |
| 16–17 | 2 | Slot count | Used; includes deleted records |
| 18–19 | 2 | Free space | Used |
| 20–21 | 2 | Dead bytes | Used |
| 22–23 | 2 | Prefix offset | Written only by tests and split inheritance ([P13](./v2-known-issues.md#p13)) |
| 24–25 | 2 | Prefix length | Always 0 in the tree path ([P13](./v2-known-issues.md#p13)) |
| 26–27 | 2 | High-key offset | **Reserved, never written** ([P2](./v2-known-issues.md#p2)) |
| 28–29 | 2 | High-key length | **Reserved, never written** ([P2](./v2-known-issues.md#p2)) |
| 30–31 | 2 | Padding | Unused |

Why 32 bytes: 16 cannot hold the prefix and space-accounting fields; 64 would cost 32 more payload bytes per page.

> **As built:** header offsets are 16-bit while slot offsets are 31-bit, two widths for the same concept ([P10](./v2-known-issues.md#p10)).

---

<a id="s4"></a>
## 4. The version word

The first 8 bytes carry three facts, so one atomic load gives a reader a consistent snapshot.

```
Bit 63                                              Bit 2   Bit 1      Bit 0
┌──────────────────────────────────────────────────┬───────┬──────────┬───────┐
│            Version counter (62 bits)             │       │ Obsolete │ Lock  │
└──────────────────────────────────────────────────┴───────┴──────────┴───────┘
```

| Bit(s) | Meaning when set |
|---|---|
| 0 | A writer holds the page |
| 1 | The page is dead (merged away) |
| 2–63 | Count of completed writes |

The counter moves in steps of 4, so a step never touches bits 0 and 1.

```
0x10  counter 4, unlocked
  lock        → 0x11  counter 4, locked
  unlock      → 0x14  counter 5, unlocked      (add 4, clear bit 0)
  lock        → 0x15  counter 5, locked
  mark dead   → 0x1A  counter 6, unlocked, obsolete   (add 4, clear bit 0, set bit 1)
```

At a billion writes per second to one page, the counter takes about 146 years to wrap.

> **As built:** the word is accessed by reinterpreting the first 8 bytes of the byte array as a 64-bit integer. It is 8-byte aligned only because the array happens to be the struct's first field ([P3](./v2-known-issues.md#p3)).

---

<a id="s5"></a>
## 5. The slot descriptor: 8 bytes

```
Bit 63   Bit 62                        Bit 32   Bit 31         Bit 16   Bit 15          Bit 0
┌───────┬─────────────────────────────────────┬─────────────────────────┬────────────────────┐
│ Ext   │ Offset (31 bits)                    │ Suffix length (16 bits) │ Value length (16)  │
└───────┴─────────────────────────────────────┴─────────────────────────┴────────────────────┘
```

| Field | Bits | Meaning |
|---|---|---|
| External | 63 | Value lives outside the page; the record holds an 8-byte handle |
| Offset | 32–62 | Byte position of the record in the page |
| Suffix length | 16–31 | Key bytes stored in the record |
| Value length | 0–15 | Value length; **0 means deleted** |

Design choices:
- **One fixed-width integer:** slot N sits at byte `32 + 8 × N`; eight slots per 64-byte cache line; rewriting a slot is one 8-byte store.
- **Value length at bit 0:** the most-checked field extracts with no shift.
- **External flag at bit 63:** the sign bit, testable in one instruction.

> **As built:**
> - A value of length 0 cannot be stored: it is indistinguishable from a deleted record ([P6](./v2-known-issues.md#p6)).
> - The offset field is 31 bits for a page that needs 12 ([P9](./v2-known-issues.md#p9)).
> - The external-value path is implemented in the page code, but no arena exists and nothing in the tree creates external records ([P7](./v2-known-issues.md#p7), [P8](./v2-known-issues.md#p8)).

---

<a id="s6"></a>
## 6. Records

A record is the key suffix immediately followed by the value, with no length bytes.

```
Inline record:      [ key suffix ][ value bytes ]
External record:    [ key suffix ][ 8-byte arena handle ]      (never created by the tree)
```

The directory is sorted by key; the heap is in insertion order. The two orders are independent, so records can be deleted or moved without re-sorting data.

```
SLOT DIRECTORY (sorted)                 HEAP (insertion order, grows ←)
┌───────────────────────┐
│ slot 0 → "apple"      │ ──────────────────────────────────┐
│ slot 1 → "banana"     │ ─────────────────────┐            │
│ slot 2 → "cherry"     │ ───────┐             │            │
└───────────────────────┘        ▼             ▼            ▼
                         ┌───────────────┬─────────────┬─────────────┐
                         │ cherry|val_c  │ banana|...  │ apple|val_a │
                         └───────────────┴─────────────┴─────────────┘
```

> **As built:** there is no maximum key or value size check and no overflow path. A record that cannot fit in an empty page is silently dropped ([P42](./v2-known-issues.md#p42)).

---

<a id="s7"></a>
## 7. Bidirectional growth

The directory grows right by 8 bytes per record; the heap grows left by the record's size. The page never stores where the heap ends; it derives it:

```
heap bottom = 32 + 8 × slotCount + freeSpace
```

> **As built:** nothing asserts this invariant; any drift silently corrupts the heap ([P11](./v2-known-issues.md#p11)).

### Insert walkthrough (prefix `/data/`, 6 bytes, as in the split test)

| Step | Record | Written at | Free space after |
|---|---|---|---|
| Empty page | — | — | 4,064 |
| Prefix installed | `/data/` | 4090–4095 | 4,058 |
| Insert `apple` + `val_a` (10 B) | slot 0 | 4080–4089 | 4,040 |
| Insert `banana` + `val_b` (11 B) | slot 1 | 4069–4079 | 4,021 |

Each new record costs its size **plus 8 bytes** for its slot.

> **As built:** the tree's pre-insert capacity check counts slot bytes twice, so pages are declared full early ([P21](./v2-known-issues.md#p21)). The page's own insert also carries a redundant size check ([P12](./v2-known-issues.md#p12)).

### Why not other layouts

| Alternative | Why it fails for variable-length records |
|---|---|
| Fixed-size records | Wastes space on small records, cannot hold large ones, sorted insert moves whole records |
| Two fixed regions | The size ratio must be guessed; one region fills while the other sits empty |
| Heap kept in key order, no directory | Middle inserts move kilobytes; no binary search over variable lengths |
| Append-only log in the page | Cheap inserts, linear-scan lookups |

Bidirectional growth lets both regions share one gap, so the page adapts to any mix of record sizes.

---

<a id="s8"></a>
## 8. Space accounting

| Kind | Tracked by | Usable for a new record |
|---|---|---|
| Contiguous gap | free space | Yes |
| Holes in the heap | dead bytes | After compaction |
| Slots of deleted records (8 B each) | nothing | After compaction |

```
4,064 = free space + 8 × slotCount + prefix length + live record bytes + dead bytes
```

Free space only shrinks in normal operation; only compaction gives space back.

> **As built:** the identity can drift. Resetting the prefix on an empty page strands its bytes ([P18](./v2-known-issues.md#p18)); an update to an empty value counts dead bytes differently from a delete ([P20](./v2-known-issues.md#p20)). Compaction recomputes free space from the layout and erases the drift.

---

<a id="s9"></a>
## 9. Prefix compression

> **As built: implemented at page level, unused by the tree.** The root page is created with prefix length 0, splits inherit it, and nothing ever computes a prefix from real keys. Only tests install prefixes ([P13](./v2-known-issues.md#p13)). Everything below describes the page-level mechanism.

```
Full keys:                           Stored on the page:
/registry/pods/default/nginx-7f9c    prefix  "/registry/pods/default/"  (once, top of heap)
/registry/pods/default/nginx-8a1d    suffix  "nginx-7f9c"
/registry/pods/default/redis-44bc    suffix  "nginx-8a1d"
                                     suffix  "redis-44bc"
```

- The prefix bytes sit at the top of the heap; the header stores their offset and length.
- Sorting by suffix is correct because every key on the page shares the prefix.
- **Stripping** the prefix from an incoming key is free (a view into the caller's bytes). **Restoring** a full key copies prefix and suffix into a new buffer ([P16](./v2-known-issues.md#p16)).
- A key that does not share the prefix cannot be stored on the page; v2 splits instead. Shortening the prefix is anticipated by a parameter of the capacity check that is always 0 ([P14](./v2-known-issues.md#p14)).
- A split copies the parent page's prefix to the new page unchanged, even when a longer one would fit the narrower range ([P15](./v2-known-issues.md#p15)).

---

<a id="s10"></a>
## 10. Mutations

All mutations run with the page's write lock held.

### Insert

1. Binary-search for the key; refuse if present (including as a deleted record).
2. Check that the record plus one slot fits in the gap.
3. Write the record at the heap bottom.
4. Shift later slots one position right.
5. Write the new slot (the record becomes visible here).
6. Update slot count and free space.

### Update: three paths

```
                   Is the existing record deleted?
                    /                         \
                  yes                          no
                   │                            │
          RESURRECT                  Both inline and new value ≤ old?
          fresh record at              /                    \
          heap bottom (slot          yes                     no
          already exists)              │                      │
                                IN PLACE               OUT OF PLACE
                                overwrite value;       rewrite whole record at heap
                                leftover → dead        bottom, move slot; old → dead
```

### Delete

Set the slot's value length to 0; nothing moves; the record's bytes become dead. The slot stays because concurrent readers may hold slot positions and binary search still compares against the deleted key.

> **As built:** deletes are tombstones ([P6](./v2-known-issues.md#p6)); an update to an empty value creates an accidental tombstone ([P20](./v2-known-issues.md#p20)).

| Operation | Free space | Dead bytes | Slot count |
|---|---|---|---|
| Insert | − (record + 8) | — | +1 |
| Update in place | — | + leftover | — |
| Update out of place | − new record | + old record | — |
| Resurrect | − new record | — | — |
| Delete | — | + record | — |

A key whose value grows every update burns heap quadratically; about 85 growing updates exhaust a page.

> **As built:** no mutation path triggers compaction. When updates exhaust the gap the tree splits the page instead, and on a one-record page the update is silently lost ([P22](./v2-known-issues.md#p22)).

---

<a id="s11"></a>
## 11. Search

Binary search over the sorted directory. Each step (a **probe**) reads the middle slot, follows its offset into the heap to read that record's key, compares, and halves the range.

- Found: returns the slot index. Not found: returns the insertion position.
- Deleted records are found like live ones; each caller interprets them (Get: missing; Update: resurrect; Delete: already deleted; Insert: refuse).

Each probe makes two reads: the slot (contiguous; nearby probes reuse cache lines) and the key bytes (scattered; usually a new cache line). On a cold page the heap reads dominate.

| Cold lookup, 200 records | Cache lines |
|---|---|
| Header | 1 |
| Prefix | 1 |
| Directory | ~5 |
| Heap keys | ~8 |
| **Total** | **~15** |

> **As built:** search runs inside the optimistic read window before validation. A slot torn by a concurrent writer can yield an out-of-range offset, and the Go bounds check panics instead of letting the reader retry ([P23](./v2-known-issues.md#p23)).

---

<a id="s12"></a>
## 12. Compaction

Rebuilds a page in a 4 KiB scratch buffer, dropping deleted records and closing every hole.

1. Copy the header into scratch.
2. Place the prefix at the top of the scratch heap.
3. Walk slots in key order; skip deleted records; copy each live record tight against the previous one; write its new slot.
4. Set slot count to the live count, dead bytes to 0; recompute free space from the two cursors.
5. Copy scratch back **except the first 8 bytes** (the version word); release the lock.

```
BEFORE  slots: A  B✝  C  D        heap: [D][hole][A][B✝ dead][C][prefix]
AFTER   slots: A  C  D            heap: [D][C][A][prefix]      (one larger gap)
```

- Slot positions change.
- The heap ends up in key order.
- Accounting drift is erased.
- Possible only because nothing outside the page holds an address into the heap; every reference goes through a slot.

The version word is skipped because only atomic operations may change it; copying a stale snapshot could tear or roll back the lock.

> **As built:** compaction runs only inside split and merge; insert and update never trigger it ([P22](./v2-known-issues.md#p22)).

---

<a id="s13"></a>
## 13. Concurrency: Optimistic Lock Coupling

### Why not a reader-writer lock

Taking a read lock writes a shared counter, which invalidates that cache line on every other core; readers slow each other with no writer present. OLC readers only load the version word.

### Writers

- **Lock:** compare-and-swap the word from unlocked to locked; fails on a dead page.
- **Unlock:** add a step and clear the lock bit in one store (only the holder can be here).
- **Mark dead:** add a step, clear the lock bit, set the obsolete bit in one atomic operation.

> **As built:** mark-dead uses a compare-and-swap loop that suggests it is safe without the lock; called unlocked it would clear another writer's lock bit ([P27](./v2-known-issues.md#p27)).

### Readers

```
1. Wait until unlocked; remember the version.           (no lock taken)
2. Search and read.                                     (may see a half-written page)
3. Copy the answer into private memory.
4. Re-read the version. Changed, locked or dead → discard and retry.
                        Unchanged              → return the copy.
```

```
TIME ─────────────────────────────────────────────────────────►
Reader:  [remember v=0x14]──[search]──[copy]──[check: still 0x14?]
Writer:             [lock 0x15]──[modify]──[unlock 0x18]
                                                   └─► 0x18 ≠ 0x14 → retry
```

> **As built:**
> - "Not found" answers are returned without step 4 ([P25](./v2-known-issues.md#p25)).
> - Page bytes are read with plain loads while writers use plain stores, a data race under the Go memory model ([P26](./v2-known-issues.md#p26)).
> - There is no retry limit; a reader can starve under constant writes ([P43](./v2-known-issues.md#p43)).

### Point lookup loop

| Page read result | Action |
|---|---|
| Consistent, found | Return |
| Consistent, not found | Key greater than the page's last key and a sibling exists → move right; else missing |
| Inconsistent, page dead | Follow the sibling pointer |
| Inconsistent | Yield and retry the same page |

One version word covers the whole page, so a write to any record invalidates readers of every record on it (false conflicts).

---

<a id="s14"></a>
## 14. Page split

Runs when a record cannot be placed (no space, or the key does not share the prefix). The page is write-locked.

1. Refuse if fewer than 2 slots.
2. The full key at slot `slotCount / 2` is the **pivot**.
3. Create a new right page (unreachable, so unlocked); copy the prefix.
4. Move every live record from the pivot position onward to the right page; drop deleted ones.
5. Cut the left page's slot count to the pivot position and compact it.
6. Point the right page at the left page's old sibling, **then** point the left page at the right page.
7. Return pivot and right page to the tree.

```
BEFORE:   [ LEFT: a b c d ] ─────────────────────────► [ NEIGHBOR ]
STEP 6a:  [ LEFT: a b ]  ────────────────────────────► [ NEIGHBOR ]
                            [ RIGHT: c d ] ───────────►     ▲
STEP 6b:  [ LEFT: a b ] ──► [ RIGHT: c d ] ──────────► [ NEIGHBOR ]
```

Keys below the pivot stay left; the pivot and above go right.

> **As built:**
> - The midpoint counts deleted records, so a page with dead records in its lower half can split into an empty left page ([P29](./v2-known-issues.md#p29)).
> - After a split, the tree ignores the result of placing the new record; it can be dropped ([P19](./v2-known-issues.md#p19)).
> - The two sibling-pointer writes are plain pointer stores, not atomic ([P26](./v2-known-issues.md#p26)).

### B-link: splitting without locking the parent

Link the right page first (leaf lock only), add the pivot to the parent later (parent lock). In between, a reader that lands left, misses its key, and sees the key is greater than every key on the page follows the sibling pointer. Writers do the same. One node is locked at a time.

> **As built:** "greater than every key on the page" uses the page's **last key**, not a stored high key. Every key between that last key and the parent's separator is written to the right sibling, outside the range its parent assigns ([P28](./v2-known-issues.md#p28), [P2](./v2-known-issues.md#p2)).

---

<a id="s15"></a>
## 15. Page merge

Runs in the delete path when free space plus dead bytes on the leaf exceed 2,048.

1. Lock the right sibling too (always left then right, so no deadlock).
2. Admission: both pages' live bytes fit in one page with 128 bytes to spare.
3. Dry run (reads only): every right-page key shares the left page's prefix; total space fits. Failure changes nothing.
4. Execute: compact the left page if its gap is too small; copy every live right-page record in, re-keyed through its full key.
5. Retire: the left page's sibling skips the right page; the dead page's sibling pointer is turned **back** to the left page; the page is marked dead and handed to EBR.
6. The parent removes the dead child and the separator to its left.

```
BEFORE:  [LEFT] ──► [RIGHT] ──► [NEXT]
AFTER:   [LEFT] ──────────────► [NEXT]
            ▲
            └────── [RIGHT, dead]
```

A reader stranded on the dead page follows its pointer back to the left page and finds its key.

> **As built:**
> - The result of locking the right page is ignored; a dead right page is merged unlocked ([P30](./v2-known-issues.md#p30)).
> - The dry run sizes records with the right page's suffix length, not the left's ([P31](./v2-known-issues.md#p31)).
> - Scan follows the back-pointer too and returns duplicates ([P32](./v2-known-issues.md#p32)).
> - The right sibling may belong to a different parent, which then keeps pointing at the dead page ([P33](./v2-known-issues.md#p33)).
> - The 128-byte headroom is small enough for a merged page to split again soon ([P44](./v2-known-issues.md#p44)).

---

<a id="s16"></a>
## 16. Tree layer

### Internal nodes

Internal nodes are Go objects, not pages: a list of separator keys and a list of children (N keys, N+1 children, at most 64 and 65). Odd version = locked; each lock or unlock adds 1; no dead state.

Routing picks the first separator strictly greater than the key; a key equal to a separator goes right.

```
keys:      [   "d"   ,   "m"   ]
children:  [ c0   ,   c1   ,   c2 ]
           below d   d..m    m and above
```

Writers never modify the lists in place; they build new lists and swap them in (copy-on-write). Each insert allocates two lists of up to 65 entries.

> **As built:** swapping a Go slice is a three-word write, not atomic, so a reader can see a torn list ([P34](./v2-known-issues.md#p34)).

### Insert flow

```
DESCENT (no locks)
root ──► internal ──► internal ──► leaf
                                     │  lock leaf (move right if needed)
                                     │  update, insert, or split
ASCENT (only after a split)          ▼
            internal ◄──── pivot + new right page
            lock, recompute position, insert pivot
            more than 64 keys? split; middle key moves up
root ◄──────┘
tree: lock root pointer, new root with one key and two children
```

Leaf splits **copy** the pivot up (it is a record). Internal splits **move** the middle key up (it is only a signpost). The tree grows only at the top.

> **As built:**
> - Every operation takes the root lock's read side to load the root ([P35](./v2-known-issues.md#p35)).
> - A new root is built from the current root pointer, not from the node that split; two racing root splits build the wrong tree ([P36](./v2-known-issues.md#p36)).
> - Nothing ever lowers the tree's height; internal nodes can be left with one child ([P41](./v2-known-issues.md#p41)).

### Delete flow

Descend, tombstone the record in the leaf, possibly merge (section 15); on the way up the parent drops any dead child.

### Range scan

Descend once to the leaf for the start key, then walk right along the sibling chain, skipping deleted records and keys below the start, stopping at the first key at or past the end.

> **As built:**
> - Scan performs no optimistic validation on leaves ([P37](./v2-known-issues.md#p37)).
> - It walks the first leaf from slot 0 instead of searching for the start ([P24](./v2-known-issues.md#p24)).
> - It allocates the full key twice per result ([P17](./v2-known-issues.md#p17)).
> - External values would be returned as a view into live page memory ([P8](./v2-known-issues.md#p8)).

---

<a id="s17"></a>
## 17. Epoch-Based Reclamation

### Problem

A merged-away page cannot be freed immediately: a lock-free reader may still be on it. A per-page reader count would require a write on every read.

### Idea

Track **when** each thread started its current operation, not where it is. Once a page is unlinked, operations that start later cannot reach it; free it once every operation already running has finished.

### State

- Global epoch counter.
- 128 per-thread slots ("active since epoch E" or inactive), each padded to its own 64-byte cache line to avoid false sharing.
- Three retire bins.

| Operation | When | What it does |
|---|---|---|
| Enter | Start of every tree operation | Record the current epoch in the thread's slot, re-checking it did not move |
| Exit | End of every tree operation | Mark the slot inactive |
| Retire | A merge kills a page | Add the page to the current epoch's bin |
| Advance | Every 50 ms | If no active slot is older than the current epoch, advance it and free the bin from two epochs back |

### Worked example

```
epoch 5   A starts (slot A = 5), stands on page X
          a merge kills X            → X filed under epoch 5
tick      nobody older than 5        → epoch 6; X NOT freed (A may still be on it)
epoch 6   B starts (slot B = 6); B can never reach X
tick      A (5) is older than 6      → no advance
          A finishes
tick      nobody older than 6        → epoch 7; epoch 5's bin freed → X freed
```

A page retired in epoch E is freed after two advances: the first proves nobody is older than E, the second proves nobody is still in E. All pages retired in one epoch are freed together.

> **As built:**
> - The reclaimed list is discarded; the Go garbage collector does the real freeing. EBR protects nothing in v2.
> - Slot numbers wrap at 128, so thread 129 shares slot 1 ([P38](./v2-known-issues.md#p38)).
> - Callers that never register all use slot 0 ([P38](./v2-known-issues.md#p38)).
> - Each tree starts a background goroutine that only stops on `Close`; benchmarks never call it ([P39](./v2-known-issues.md#p39)).

---

<a id="s18"></a>
## 18. Where v2 allocates memory

| Source | When | Size |
|---|---|---|
| New right page | Every leaf split | 4,864 B size class |
| Compaction scratch buffer | Split, merge | 4 KiB |
| New key and child lists | Every internal-node insert | Up to 65 entries each |
| Separator keys | Every pivot | One object per key |
| Full-key reconstruction | Scan, split, merge, move-right checks | Per call ([P16](./v2-known-issues.md#p16)) |
| Value copy | Every successful lookup | Per call |
| Scan result key and value | Every scan result | Two per result ([P17](./v2-known-issues.md#p17)) |

This table is the concrete reason v3 moves pages into an unmanaged arena.

---

<a id="s19"></a>
## 19. Limits and numbers

| Quantity | Value |
|---|---|
| Page size | 4,096 B (object: 4,104 B; allocation: 4,864 B) |
| Header | 32 B |
| Usable bytes | 4,064 B |
| Slot size | 8 B (8 per cache line) |
| Largest value alone on a page, 1-byte key | 4,055 B |
| Most records per page (1-byte key and value) | 406 (~80% of space is slots) |
| Binary search steps on a full page | at most 9 |
| Internal node fanout | 64 keys, 65 children |
| Merge trigger | more than 2,048 B reclaimable |
| Merge headroom | 128 B |
| EBR thread slots | 128 |
| EBR tick | 50 ms |

---

<a id="s20"></a>
## 20. Glossary

| Term | Meaning |
|---|---|
| Slot | Fixed 8-byte descriptor pointing to one record |
| Heap | Region at the end of the page holding record bytes |
| Suffix | The part of a key stored in a record after removing the page prefix |
| Tombstone | Deleted record whose slot remains with value length 0 |
| Dead bytes | Heap bytes no live record uses |
| Probe | One step of binary search |
| Pivot / separator | Key dividing two sibling pages; lives in the parent |
| OLC | Optimistic Lock Coupling: read without locking, validate afterwards |
| Obsolete | A page merged away that must not be used |
| B-link | B-tree whose nodes link to their right sibling, so splits need not lock the parent |
| Epoch | One tick of the reclamation clock |
| Cache line | 64-byte unit in which memory moves between RAM and CPU caches |
