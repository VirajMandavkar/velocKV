# VelocaKV v2 Known Issues

> Every flaw found in v2, with evidence you can check in the source, the trigger that exposes it, and how v3 resolves it.
> The mechanisms themselves are described once, in [`v2-architecture.md`](./v2-architecture.md); each issue links back to the section it affects.
> v3 references point to [`v3-architecture-plan.md`](./v3-architecture-plan.md).

## How to read an entry

| Field | Meaning |
|---|---|
| **Type** | Bug (wrong behavior), Debt (works but costs or blocks something), Not a flaw |
| **Severity** | Critical: data loss or crash · High: wrong results or broken invariant · Medium: latent bug, performance, or test integrity · Low: design debt · None |
| **Evidence** | File and function where the behavior lives |
| **Trigger** | The shortest sequence that exposes it |
| **Verify** | A test that fails on v2 and must pass on v3 |

## Totals

| | Count |
|---|---|
| Bugs | 24 (4 Critical: P19, P22, P23, P42) |
| Debts | 21 |
| Not a flaw | 1 |
| **Total** | **46** |

P1–P39 were found during the v2 masterclass; P40–P44 while writing these documents; P45–P46 from race-detector and correctness-guard evidence.

## Summary

| # | Title | Type | Severity | v3 status |
|---|---|---|---|---|
| [P1](#p1) | Header sibling bytes unused | Debt | Low | Fixed |
| [P2](#p2) | High-key fields never written | Debt | Medium | Fixed |
| [P3](#p3) | Version word alignment by accident | Debt | Medium | Fixed |
| [P4](#p4) | No page constructor | Debt | Low | Fixed |
| [P5](#p5) | 4 KiB alignment benefit not realised | Debt | Low | Fixed |
| [P6](#p6) | Empty value equals deleted | Bug | High | Fixed |
| [P7](#p7) | Value length has two meanings | Debt | Low | Fixed |
| [P8](#p8) | External path dead; Scan returns handle bytes | Bug | Medium | Fixed |
| [P9](#p9) | 19 wasted bits per slot | Debt | Low | Fixed |
| [P10](#p10) | Inconsistent offset widths | Debt | Low | Fixed |
| [P11](#p11) | Heap boundary derived, never asserted | Debt | Low | Fixed |
| [P12](#p12) | Redundant size check | Not a flaw | None | — |
| [P13](#p13) | Prefix compression never used by the tree | Debt | Low | Fixed |
| [P14](#p14) | Prefix-shrink parameter always 0 | Debt | Low | Obsolete |
| [P15](#p15) | Split never lengthens the prefix | Debt | Low | Fixed |
| [P16](#p16) | Full-key rebuild allocates per call | Debt | Medium | Fixed |
| [P17](#p17) | Scan allocates each key twice | Debt | Medium | Fixed |
| [P18](#p18) | Prefix bytes stranded on reset | Bug | Medium | Fixed |
| [P19](#p19) | Post-split insert failures ignored | Bug | **Critical** | Fixed |
| [P20](#p20) | Empty-value update makes an accidental tombstone | Bug | High | Fixed |
| [P21](#p21) | Capacity check counts slots twice | Bug | Medium | Fixed |
| [P22](#p22) | Growing update lost or duplicated | Bug | **Critical** | Fixed |
| [P23](#p23) | Torn slot read panics | Bug | **Critical** | Fixed |
| [P24](#p24) | Scan walks the first leaf from slot 0 | Debt | Medium | Fixed |
| [P25](#p25) | "Not found" returned unvalidated | Bug | High | Fixed |
| [P26](#p26) | Plain reads race with plain writes | Bug | High | Fixed |
| [P27](#p27) | Mark-dead suggests unlocked safety | Debt | Low | Fixed |
| [P28](#p28) | Last key used as the page fence | Bug | High | Fixed |
| [P29](#p29) | Split midpoint counts tombstones | Bug | High | Fixed |
| [P30](#p30) | Merge ignores right-page lock result | Bug | Medium | Fixed |
| [P31](#p31) | Merge dry run uses the wrong suffix length | Bug | Medium | Fixed |
| [P32](#p32) | Scan follows dead page back-pointer | Bug | High | Fixed |
| [P33](#p33) | Merge across parents | Bug | High | Fixed |
| [P34](#p34) | Internal-node slice swap tears | Bug | High | Fixed |
| [P35](#p35) | Root read takes a shared lock | Debt | Medium | Fixed |
| [P36](#p36) | Racing root splits build the wrong tree | Bug | High | Fixed |
| [P37](#p37) | Scan has no optimistic validation | Bug | High | Fixed |
| [P38](#p38) | EBR slots wrap at 128 | Bug | High | Fixed |
| [P39](#p39) | Benchmarks leak whole trees | Bug | High | Fixed |
| [P40](#p40) | Page object lands in a 4,864 B size class | Debt | Medium | Fixed |
| [P41](#p41) | Tree height never shrinks | Debt | Low | **Open** |
| [P42](#p42) | Oversized records silently dropped | Bug | **Critical** | Fixed |
| [P43](#p43) | Unbounded optimistic retries | Debt | Medium | Fixed |
| [P44](#p44) | Merge headroom too small | Debt | Medium | Fixed |
| [P45](#p45) | Version word copied with plain memory ops | Bug | Medium | Fixed |
| [P46](#p46) | Mixed workload corrupts key order | Bug | High | Fixed |

---

## Page layout and header

<a id="p1"></a>
### P1 · Header sibling bytes are unused

**Type:** Debt · **Severity:** Low · **Affects:** [architecture §3](./v2-architecture.md#s3)
**Evidence:** `page_layout.go` defines `OffsetRightSibling = 8`; no function reads or writes it. The sibling is the `next *SlottedPage` field in `slotted_page.go`.
**Trigger:** Any page. Bytes 8–15 are always zero.

**What happens.** The header reserves 8 bytes for a sibling reference that nothing uses, while the real sibling lives outside the 4 KiB array as a Go pointer. The page image is not self-describing: copying the 4,096 bytes loses the sibling.

**Impact.** 8 wasted header bytes per page; blocks any on-disk or arena format, because a Go pointer cannot be stored in raw memory the collector does not scan.

**v3 resolution.** `rightSib` at header offset 24 holds a `SlotDescriptor` (page ID + generation), an integer valid in raw arena memory (v3 plan § Page geometry).

**Verify.** Split a page, copy only its 8,192 bytes to a fresh buffer, resolve `rightSib`: it must reach the new right page.

<a id="p2"></a>
### P2 · High-key fields are reserved but never written

**Type:** Debt · **Severity:** Medium · **Affects:** [§3](./v2-architecture.md#s3), [§14](./v2-architecture.md#s14)
**Evidence:** `page_layout.go` defines `OffsetHighKeyOffset = 26` and `OffsetHighKeyLen = 28`; no accessor exists in `slotted_page.go`.
**Trigger:** Any page. The fields are always zero.

**What happens.** B-link trees need each page to know its upper bound (high key) so move-right decisions are exact. v2 reserved the space but never implemented it.

**Impact.** Every move-right decision falls back to the page's last key, which causes [P28](#p28). Fence-derived prefixes are impossible without stored fences.

**v3 resolution.** Low and high fences are stored in the heap, with offsets and lengths in the header (offsets 44–51) (v3 plan § Page geometry).

**Verify.** After a split, left page's high fence equals right page's low fence equals the separator in the parent.

<a id="p3"></a>
### P3 · Version word is 8-byte aligned by accident

**Type:** Debt · **Severity:** Medium · **Affects:** [§4](./v2-architecture.md#s4)
**Evidence:** `slotted_page.go` → `versionPtr` reinterprets `&p.data[0]` as `*uint64` via `unsafe`; the `data` array is the struct's first field.
**Trigger:** Reorder the struct so any field precedes `data`, or build for a 32-bit target.

**What happens.** 64-bit atomic operations require 8-byte alignment. A byte array has alignment 1; this works only because the array is the first field of a heap object whose start is aligned.

**Impact.** A harmless refactor can turn every atomic operation on the version word into a crash on platforms that fault on misaligned atomics, with no compile-time warning.

**v3 resolution.** Pages live in `mmap`'d slabs whose base is OS-page aligned; page offsets and header word offsets are multiples of 8, asserted once at arena initialisation (v3 plan § Arena).

**Verify.** Arena init test asserts `base % 4096 == 0` and that every header word offset is a multiple of 8.

<a id="p4"></a>
### P4 · No page constructor

**Type:** Debt · **Severity:** Low · **Affects:** [§2](./v2-architecture.md#s2)
**Evidence:** The sequence set-slot-count(0), set-free-space(4064), set-prefix is repeated in `tree.go` → `Put` (root creation), `slotted_page.go` → `Split`, and every test in `slotted_page_test.go`.
**Trigger:** Any change to initial page state must be made in several places.

**What happens.** Page initialisation is spread across callers. Forgetting one field (for example dead bytes, which `Put` does not reset) is easy.

**Impact.** Inconsistent initial state; tests exercise a different setup path than production.

**v3 resolution.** A single `initPage(kind, lowFence, highFence)` sets every header field and places the fences.

**Verify.** `page.Check()` passes immediately after `initPage` for every kind.

<a id="p5"></a>
### P5 · The 4 KiB alignment benefit is not realised

**Type:** Debt · **Severity:** Low · **Affects:** [§2](./v2-architecture.md#s2)
**Evidence:** `slotted_page.go` → `SlottedPage` is allocated with `&SlottedPage{}` in `tree.go` → `Put` and `slotted_page.go` → `Split`.
**Trigger:** Print a page's address modulo 4096; it is almost never 0.

**What happens.** The 4 KiB size was chosen so a page maps to one OS page and one TLB entry. The Go allocator places objects in size-class spans with no 4 KiB alignment, so a page usually straddles two OS pages.

**Impact.** The claimed TLB benefit is theoretical. See also [P40](#p40).

**v3 resolution.** `mmap`'d slabs, page-aligned addresses, optional huge pages (v3 plan § Arena).

**Verify.** Every resolved page address is a multiple of the page size.

<a id="p40"></a>
### P40 · Page object lands in a 4,864-byte size class

**Type:** Debt · **Severity:** Medium · **Affects:** [§2](./v2-architecture.md#s2), [§18](./v2-architecture.md#s18)
**Evidence:** `slotted_page.go` → `SlottedPage` is a 4,096-byte array plus an 8-byte pointer = 4,104 bytes. The Go runtime's size classes step from 4,096 to 4,864 bytes.
**Trigger:** Any page allocation.

**What happens.** The extra 8-byte pointer pushes each page just past 4,096 bytes, so the allocator rounds it up to the next size class.

**Impact.** 760 bytes (15.6% of each allocation) wasted per page; memory per page is 4,864 B, not 4,096 B. Also because the object contains a pointer, it cannot be allocated as pointer-free memory.

**v3 resolution.** Pages are exactly `PageSize` bytes of arena memory with no Go object wrapper; the sibling is an integer inside the header.

**Verify.** Allocate 10,000 v2 pages and compare heap growth with 10,000 × 4,096; the ratio is ≈ 1.19. v3 arena usage per page is exactly `PageSize`.

<a id="p10"></a>
### P10 · Header and slot use different offset widths

**Type:** Debt · **Severity:** Low · **Affects:** [§3](./v2-architecture.md#s3)
**Evidence:** `slotted_page.go` → prefix offset accessors use 16-bit fields; `page_layout.go` → `PackSlot` stores a 31-bit offset.
**Trigger:** Any code that moves an offset between header and slot converts widths.

**What happens.** The same concept (a byte position in the page) has two encodings.

**Impact.** Conversion noise and room for truncation bugs if pages ever exceed 64 KiB.

**v3 resolution.** 13-bit offsets in slots and 16-bit header fields, both covering 4 and 8 KiB pages consistently (v3 plan § Slot format).

**Verify.** Lint test: every offset field in header and slot is derived from `PageBits`.

---

## Slot descriptor and records

<a id="p6"></a>
### P6 · An empty value is indistinguishable from a deleted key

**Type:** Bug · **Severity:** High · **Affects:** [§5](./v2-architecture.md#s5), [§10](./v2-architecture.md#s10)
**Evidence:** `slotted_page.go` → `DeleteRecord` marks deletion by writing value length 0; `GetRecord` treats value length 0 as not found; `tree.go` → `Scan` skips value length 0.
**Trigger:** `Put(k, empty value)` → slot stored with value length 0 → `Get(k)` reports not found; `Delete(k)` returns false; `Scan` omits `k`.

**What happens.** The deletion marker is a legal value length. Storing an empty value creates a tombstone.

**Impact.** Silent data loss for any caller that stores empty values (common for sets and markers, e.g. etcd keys used as flags).

**v3 resolution.** Delete removes the slot entirely; value length 0 is an ordinary empty value (v3 plan § Mutations).

**Verify.** `Put(k, "")` then `Get(k)` returns found with an empty value; `Scan` includes `k`.

<a id="p7"></a>
### P7 · Value length has two meanings

**Type:** Debt · **Severity:** Low · **Affects:** [§5](./v2-architecture.md#s5)
**Evidence:** `slotted_page.go` → `InsertRecord` stores the caller's value length in the slot but writes an 8-byte handle when external; `Compact`, `DeleteRecord`, `UpdateRecord` and `Split` each special-case external records to use 8.
**Trigger:** Any external record (none are created by the tree; see [P8](#p8)).

**What happens.** For inline records the field is the stored size; for external records it is the logical size of data elsewhere, and every size calculation must branch.

**Impact.** Five places must remember the special case; values over 65,535 bytes cannot be described at all.

**v3 resolution.** No external flag. Overflow records store `headPageID + totalLen` as an 8-byte inline value, with the overflow bit set; the slot's value length is always the stored size (v3 plan § Records).

**Verify.** For every slot, `offset + keyLen + valLen` equals the end of its stored bytes, with no branch on kind.

<a id="p8"></a>
### P8 · External-value path is dead, and Scan would leak handle bytes

**Type:** Bug · **Severity:** Medium (latent) · **Affects:** [§5](./v2-architecture.md#s5), [§16](./v2-architecture.md#s16)
**Evidence:** No caller in `tree.go` passes `isExt = true`. `tree.go` → `Scan` returns, for external records, a slice of `leaf.data` covering the 8-byte handle, without copying.
**Trigger:** Insert a record with the external flag set via `InsertRecord`, then `Scan` over it.

**What happens.** Scan returns the arena handle as if it were the value, and the returned slice aliases live page memory, so a later write to the page changes bytes the caller holds.

**Impact.** Latent today; wrong values and aliasing bugs the moment external values are enabled.

**v3 resolution.** The external path is removed; overflow values are copied out of the chain into worker-owned memory and validated (v3 plan § Records, § Zero-alloc API).

**Verify.** Store a 100 KiB value, scan it, compare byte-for-byte; mutate the page after the callback and confirm the caller's earlier copy is unaffected.

<a id="p9"></a>
### P9 · Slot offset field wastes 19 bits

**Type:** Debt · **Severity:** Low · **Affects:** [§5](./v2-architecture.md#s5)
**Evidence:** `page_layout.go` → `SlotOffsetMask` covers 31 bits; a 4,096-byte page needs 12.
**Trigger:** Any slot.

**What happens.** The offset was sized for pages up to 2 GiB.

**Impact.** No room for a key head, so every binary-search step reads the heap.

**v3 resolution.** 13-bit offset; freed bits carry a 24-bit key head (v3 plan § Slot format).

**Verify.** Heads on/off ablation benchmark on cold lookups.

<a id="p42"></a>
### P42 · Oversized records are silently dropped

**Type:** Bug · **Severity:** Critical · **Affects:** [§6](./v2-architecture.md#s6)
**Evidence:** `tree.go` → `Put` ignores the result of `InsertRecord` when creating the root; `putRecursive` ignores `insertIntoLeaf` results after `Split`. No size check exists anywhere.
**Trigger:** `Put(k, 5,000-byte value)` on an empty tree → root created, `InsertRecord` returns false, key gone, no error. On a non-empty tree: update fails, capacity check fails, split, `insertIntoLeaf` fails on both halves, key gone.

**What happens.** A record larger than an empty page's capacity (4,055 bytes of value with a 1-byte key) can never be placed, and nothing reports it.

**Impact.** Silent data loss for large values, exactly the etcd pod-spec range.

**v3 resolution.** `MaxKeyLen = 512`; records over `MaxInlineRecord` (1,760 B at 8 KiB) move the value to an overflow chain; values over 1.5 MiB return an error (v3 plan § Records).

**Verify.** Put values of 1,760, 1,761, 100 KiB and 1.5 MiB; all read back exactly; 1.5 MiB + 1 returns an error.

---

## Space accounting and mutations

<a id="p11"></a>
### P11 · Heap boundary is derived and never asserted

**Type:** Debt · **Severity:** Low · **Affects:** [§7](./v2-architecture.md#s7)
**Evidence:** `slotted_page.go` → `InsertRecord` and `UpdateRecord` compute the heap bottom as header + slots + free space; no check compares it with the lowest record offset.
**Trigger:** Any bug that changes free space without writing bytes (for example [P18](#p18)).

**What happens.** The heap boundary exists only as arithmetic over two counters.

**Impact.** Accounting drift silently corrupts records instead of failing loudly.

**v3 resolution.** `heapStart` is a stored header field; free space is derived from it; `page.Check()` asserts every record lies in `[heapStart, PageSize)` (v3 plan § Page geometry).

**Verify.** Fuzz page operations with `page.Check()` after each.

<a id="p12"></a>
### P12 · Redundant maximum-payload check

**Type:** Not a flaw · **Severity:** None · **Affects:** [§7](./v2-architecture.md#s7)
**Evidence:** `slotted_page.go` → `InsertRecord` checks against `maxAllowedPayload` before the free-space check, which already implies it.
**Trigger:** None.

**What happens.** Two checks, one sufficient. Harmless.

**v3 resolution.** One capacity rule.

<a id="p18"></a>
### P18 · Prefix bytes are stranded when the prefix is reset

**Type:** Bug · **Severity:** Medium (latent) · **Affects:** [§8](./v2-architecture.md#s8)
**Evidence:** `tree.go` → `insertIntoLeaf` sets prefix length to 0 on an empty page with a mismatching key, but neither restores free space nor adds the prefix bytes to dead bytes.
**Trigger:** A page with a prefix becomes empty; a key without that prefix is inserted. (Unreachable in the tree today because prefixes are always empty, [P13](#p13).)

**What happens.** The prefix bytes remain at the top of the heap, counted by nothing.

**Impact.** Space leak until the next compaction; breaks the accounting identity.

**v3 resolution.** The prefix has no bytes of its own; it is the first `prefixLen` bytes of the low fence, which changes only on split and merge (v3 plan § Page geometry).

**Verify.** `page.Check()` identity holds after every operation in `FuzzPage`.

<a id="p20"></a>
### P20 · Updating to an empty value creates an accidental tombstone

**Type:** Bug · **Severity:** High · **Affects:** [§8](./v2-architecture.md#s8), [§10](./v2-architecture.md#s10)
**Evidence:** `slotted_page.go` → `UpdateRecord` takes the in-place path for any new value no longer than the old, including length 0, and adds only the old value length to dead bytes. `DeleteRecord` adds suffix + value.
**Trigger:** `Put(k, "abc")` then `Put(k, "")` → `Get(k)` reports not found; dead bytes are 3 lower than a `Delete(k)` would have produced.

**What happens.** Two paths create tombstones with different accounting.

**Impact.** Data disappears ([P6](#p6)); dead-byte totals undercount, skewing merge triggers.

**v3 resolution.** Fixed with P6: an empty value is a normal value; delete is slot removal with one accounting rule.

**Verify.** Model-based fuzz comparing `deadBytes` against a recomputed value.

<a id="p21"></a>
### P21 · The tree's capacity check counts slot bytes twice

**Type:** Bug · **Severity:** Medium · **Affects:** [§7](./v2-architecture.md#s7)
**Evidence:** `tree.go` → `putRecursive` passes `4064 − free − dead` as active payload to `EvaluateCapacity` in `slotted_page.go`. That figure already includes slot bytes and the prefix; `EvaluateCapacity` adds header and all slots again.
**Trigger:** A page with 200 slots: 1,600 phantom bytes; the insert is refused and the page splits while ~40% of it is still free.

**What happens.** Inserts are rejected early, and when the check fails the tree splits without even trying `InsertRecord`.

**Impact.** Premature splits → more pages, lower fill, more allocations.

**v3 resolution.** A single capacity rule inside the page: `need ≤ freeSpace`, else `need ≤ freeSpace + deadBytes` → compact, else split (v3 plan § Mutations).

**Verify.** Fill a page with small records via the tree; fill factor at first split exceeds 95%.

<a id="p22"></a>
### P22 · A growing update is lost or duplicated

**Type:** Bug · **Severity:** Critical · **Affects:** [§10](./v2-architecture.md#s10), [§12](./v2-architecture.md#s12)
**Evidence:** `tree.go` → `putRecursive`: after `UpdateRecord` fails for lack of space, it calls `InsertRecord` (refuses: key exists), then `Split`, then `insertIntoLeaf`, which calls `InsertRecord` again rather than `UpdateRecord`. No path compacts.
**Trigger:**
- One-record page, value grows past free space → `Split` returns nothing (fewer than 2 slots) → `InsertRecord` refuses (key exists) → update lost, no error.
- Multi-record page, key ≥ pivot → after split, `InsertRecord` on the right page refuses (key exists) → update lost.
- Multi-record page, key < pivot → left page refuses, fallback inserts into the right page → the key now exists on **both** pages; `Get` routes left and returns the **old** value.

**What happens.** The only fallback for "update does not fit" is "insert", which is guaranteed to fail for an existing key. Dead bytes are never reclaimed before giving up.

**Impact.** Silent lost updates and stale reads for any key whose value grows. Dead bytes grow quadratically for such keys.

**v3 resolution.** Lazy compaction before split; after a split the mutation is retried as an update on the correct half; every mutation returns an error instead of false (v3 plan § Mutations).

**Verify.** Grow one key's value by 1 byte 10,000 times, with and without neighbours; `Get` returns the latest value every time and exactly one copy exists.

---

## Prefix compression

<a id="p13"></a>
### P13 · Prefix compression is never activated by the tree

**Type:** Debt · **Severity:** Low · **Affects:** [§3](./v2-architecture.md#s3), [§9](./v2-architecture.md#s9)
**Evidence:** `tree.go` → `Put` creates the root with prefix length 0; `slotted_page.go` → `Split` copies the parent prefix; `insertIntoLeaf` only ever sets it to 0. No code computes a prefix from keys.
**Trigger:** Insert any keys through the tree; every page has prefix length 0.

**What happens.** The page supports prefix compression; the tree never uses it. Only tests install prefixes.

**Impact.** etcd-style keys (`/registry/pods/...`) are stored in full; a 23-byte shared prefix across 100 keys wastes ~2.3 KB per page.

**v3 resolution.** Prefix = common prefix of the page's low and high fences, set automatically on every split and merge (v3 plan § Page geometry, D2).

**Verify.** Load 1M etcd-style keys; average stored suffix length is well below full key length.

<a id="p14"></a>
### P14 · The prefix-shrink parameter is always 0

**Type:** Debt · **Severity:** Low · **Affects:** [§9](./v2-architecture.md#s9)
**Evidence:** `slotted_page.go` → `EvaluateCapacity` accepts `deltaPrefixLen`; `tree.go` → `putRecursive` always passes 0.
**Trigger:** None; the path is unused.

**What happens.** A design for shrinking the prefix to admit a mismatching key exists only as a parameter.

**v3 resolution.** Obsolete: with fence-derived prefixes a routed key always shares the page prefix.

<a id="p15"></a>
### P15 · Split never lengthens the prefix

**Type:** Debt · **Severity:** Low · **Affects:** [§9](./v2-architecture.md#s9)
**Evidence:** `slotted_page.go` → `Split` copies the parent's prefix bytes verbatim to the right page and keeps it on the left.
**Trigger:** Split any prefixed page.

**What happens.** Each half covers a narrower key range and could share a longer prefix, but keeps the old one.

**Impact.** Compression never improves as the tree grows.

**v3 resolution.** Each half gets new fences, so its prefix is recomputed (v3 plan § Split).

**Verify.** Prefix length of children ≥ prefix length of the page they split from.

<a id="p16"></a>
### P16 · Full-key reconstruction allocates on every call

**Type:** Debt · **Severity:** Medium · **Affects:** [§9](./v2-architecture.md#s9), [§18](./v2-architecture.md#s18)
**Evidence:** `slotted_page.go` → `AssembleFullKey(i, nil)`; every caller passes `nil`: `Get`, `Split`, `MergeRight` (twice per record), `tree.go` → `putRecursive`, `deleteRecursive`, `Scan`.
**Trigger:** Any scan, split, merge or move-right check.

**What happens.** Each call allocates a new key buffer.

**Impact.** Allocation pressure on hot paths; feeds GC thrashing.

**v3 resolution.** Keys are assembled into worker-owned buffers (v3 plan § Zero-alloc API).

**Verify.** `-benchmem` shows 0 allocs/op for Get, Put, Delete, Scan.

---

## Search and scan

<a id="p23"></a>
### P23 · A torn slot read panics instead of retrying

**Type:** Bug · **Severity:** Critical · **Affects:** [§11](./v2-architecture.md#s11)
**Evidence:** `slotted_page.go` → `SeekSlot` slices `p.data[offset : offset+sufLen]` and `GetRecord` slices the value, both before `Validate`. Neither checks the offset against the page bounds.
**Trigger:** Concurrent `Get` while a writer shifts slots in `InsertRecord` or copies scratch back in `PublishScratch`; a slot read mid-write yields `offset + sufLen > 4096`.

**What happens.** An optimistic reader is allowed to see garbage, as long as it validates before trusting it. v2 uses the garbage (as a slice bound) before validating, and Go's bounds check panics.

**Impact.** A process crash under concurrent load; nothing recovers the panic.

**v3 resolution.** Slots and header words are loaded atomically and every offset and length is bounds-checked before use; a failed check counts as a failed validation. In a raw arena this is mandatory: an out-of-range offset would silently read a neighbouring page (v3 plan § Concurrency).

**Verify.** Torn-slot test: feed random 64-bit slot values into the read path; it must retry every time and never read outside the page.

<a id="p24"></a>
### P24 · Scan walks the first leaf from slot 0

**Type:** Debt · **Severity:** Medium · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `Scan` loops `i` from 0 over every slot of the first leaf, rebuilding and comparing each full key against the start key.
**Trigger:** Start key in the middle of a 200-record leaf → ~100 wasted key rebuilds (and allocations, [P16](#p16)).

**What happens.** The binary search that already exists in `SeekSlot` is not used to find the start position.

**Impact.** O(n) instead of O(log n) per scan on the first leaf, with allocations.

**v3 resolution.** `SeekSlot(start)` with key heads, then walk (v3 plan § Zero-alloc API).

**Verify.** Benchmark 100-key scans; time is independent of the start key's position in its leaf.

<a id="p17"></a>
### P17 · Scan allocates each key twice

**Type:** Debt · **Severity:** Medium · **Affects:** [§16](./v2-architecture.md#s16), [§18](./v2-architecture.md#s18)
**Evidence:** `tree.go` → `Scan` calls `AssembleFullKey(i, nil)` (allocation), then copies the result into a new `keyCopy` (second allocation), then allocates the value.
**Trigger:** Any scan. Three allocations per result; 100-result scans at 1M/s ≈ 300M allocations/s.

**What happens.** A key that was just allocated for the caller is allocated again.

**Impact.** The single largest source of GC pressure in v2 benchmarks.

**v3 resolution.** Rows are assembled into a worker-owned batch buffer and passed to a callback; zero allocations (v3 plan § Zero-alloc API).

**Verify.** `-benchmem` on `Scan_Ranges`: 0 allocs/op.

<a id="p37"></a>
### P37 · Scan performs no optimistic validation

**Type:** Bug · **Severity:** High · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `Scan` reads slot count, slots and record bytes of each leaf with no `ReadLockOrSpin` and no `Validate`.
**Trigger:** Scan a range while another goroutine inserts into the same leaf.

**What happens.** Scan reads pages in the middle of writes and never finds out.

**Impact.** Missing rows, duplicated rows, torn keys or values, and the [P23](#p23) panic.

**v3 resolution.** Per-leaf: record the version, copy the rows, validate, then emit; on failure re-seek from the last emitted key (v3 plan § Zero-alloc API).

**Verify.** Concurrent stress: scanners checking strict key order and no duplicates while writers insert.

---

## Concurrency

<a id="p25"></a>
### P25 · "Not found" is returned without validation

**Type:** Bug · **Severity:** High · **Affects:** [§13](./v2-architecture.md#s13)
**Evidence:** `slotted_page.go` → `GetRecord` returns `(nil, false, valid=true)` on prefix mismatch, search miss and tombstone, without calling `Validate`. `Get` then rebuilds the last key for the move-right check, also unvalidated.
**Trigger:** `Get(k)` while a writer shifts slots in the same page; the binary search skips past `k` and reports missing.

**What happens.** Only positive answers are validated.

**Impact.** False "not found" for keys that exist; incorrect move-right decisions.

**v3 resolution.** Every answer, including "not found" and "move right", is copied and validated before it is acted on (v3 plan § Concurrency).

**Verify.** Stress test: readers of a fixed set of never-deleted keys while writers insert other keys; zero misses.

<a id="p26"></a>
### P26 · Plain reads race with plain writes

**Type:** Bug · **Severity:** High · **Affects:** [§13](./v2-architecture.md#s13), [§14](./v2-architecture.md#s14)
**Evidence:** `slotted_page.go` → readers use `binary.LittleEndian` loads and slicing on `p.data`; writers use `copy` and `binary.LittleEndian.Put*`. Sibling pointers are set with plain assignments in `Split` and `MergeRight`.
**Trigger:** `go test -race` running any concurrent Get/Put workload reports a data race.
**Measured:** `go test -race` on the guarded benchmarks fails all 4 concurrent sub-benchmarks. Put-only (`Put_Random_Parallel`, 12 goroutines): 8 races, 3 distinct conflicts — sibling pointer (this item), [P34](#p34), [P45](#p45). The mixed workload adds unvalidated leaf reads in `Get` and `Scan` ([P25](#p25), [P37](#p37)).

**What happens.** Optimistic locking deliberately lets readers observe concurrent writes, but under the Go memory model a data race permits the implementation to report it and terminate, and nothing orders the data loads before the validating version load. The same issue is well known for seqlocks in C++.

**Impact.** Correctness depends on compiler and hardware behavior the language does not promise; the race detector cannot be used to find real races.

**v3 resolution.** Everything a reader branches on (version, header words, slots, descriptors) is loaded atomically; payload bytes are copied plainly and discarded unless validation passes; `race`-tagged builds make readers take the write lock so the detector checks everything else (v3 plan § Concurrency).

**Verify.** `go test -race ./...` clean under both page-size builds.

<a id="p27"></a>
### P27 · Mark-dead suggests it is safe without the lock

**Type:** Debt · **Severity:** Low · **Affects:** [§13](./v2-architecture.md#s13)
**Evidence:** `slotted_page.go` → `MarkObsolete` uses a compare-and-swap loop. Its only caller, `MergeRight`, holds the lock.
**Trigger:** Call `MarkObsolete` on a page another writer holds: the swap succeeds and clears that writer's lock bit.

**What happens.** The loop implies it tolerates concurrent writers; it does not.

**Impact.** A future caller could unlock a page mid-write.

**v3 resolution.** Assert the lock bit is held; use the same single-store pattern as unlock.

**Verify.** Debug build panics if `MarkObsolete` is called without the lock.

<a id="p43"></a>
### P43 · Optimistic retries are unbounded

**Type:** Debt · **Severity:** Medium · **Affects:** [§13](./v2-architecture.md#s13)
**Evidence:** `slotted_page.go` → `Get` and `ReadLockOrSpin`; `tree.go` → the internal-node read loops in `Get`, `putRecursive`, `deleteRecursive`, `Scan`. All loop forever with `runtime.Gosched()`.
**Trigger:** One goroutine updating a hot page continuously while others read it.

**What happens.** A reader keeps failing validation or waiting for the lock bit, with no limit and no fallback.

**Impact.** Reader starvation and CPU burn on hot pages.

**v3 resolution.** After 16 failed attempts the reader takes the write lock for that read (v3 plan § Concurrency).

**Verify.** Hot-page benchmark: reader p99 latency stays bounded while a writer loops.

<a id="p45"></a>
### P45 · The version word is copied with plain memory operations

**Type:** Bug · **Severity:** Medium · **Affects:** [§13](./v2-architecture.md#s13)
**Evidence:** `slotted_page.go` → `Compact` copies `p.data[0:HeaderSize]`, version word included, into scratch with `copy`; `Split` copies scratch back over the page with `copy`. Elsewhere the same word is accessed with `atomic.CompareAndSwapUint64` (`tree.go`, write-lock acquisition).
**Trigger:** `go test -race` with concurrent `Put`: race between `Compact` (slotted_page.go:42) and the CAS at tree.go:255.

**What happens.** A word that other goroutines access atomically is read and rewritten with plain memmove. The value survives in practice because the splitting writer holds the lock, so the snapshot equals the live value, but the language does not guarantee the store is a single untorn 8-byte write.

**Impact.** Latent: no observed corruption, but a memory-model violation on the one word the whole concurrency scheme depends on.

**v3 resolution.** The version word is only ever touched through `atomic` operations; compaction and split copy from the first byte after the header word.

**Verify.** `go test -race` concurrent-put stress test reports no race on the version word.

<a id="p46"></a>
### P46 · Mixed parallel workload corrupts key order

**Type:** Bug · **Severity:** High · **Affects:** [§14](./v2-architecture.md#s14), [§16](./v2-architecture.md#s16)
**Evidence:** `docs/v2/bench-run2-corruption.txt` line 274–275.
**Trigger:** `go test -bench MixedWorkload_Parallel -benchmem -count=10 ./engine` with the guarded benchmark (`verifyScan` after `RunParallel`). Reproduces in ~1 of 10 runs.

**What happens.** After a 12-goroutine mixed workload (40% Put, 40% Get, 15% Delete, 5% Scan), a full-range scan returned `key-00084295` before `key-00084209` — a backward jump of ~86 keys, roughly one page. The tree's sort invariant is broken.

**Impact.** Silent data corruption: a range query returns wrong results; a point lookup may miss a key that exists.

**Cause.** Unconfirmed. Candidate root causes: [P32](#p32) (scan follows dead back-pointer), [P33](#p33) (cross-parent merge), [P34](#p34) (torn inner-node swap), [P36](#p36) (racing root splits).

**v3 resolution.** Inner nodes become slotted pages in the arena; mutations use OLC write-lock; root is swapped with atomic CAS; merge is same-parent only with restart-from-root on obsolete.

**Verify.** `verifyScan` guard passes with `-count=100` on all parallel benchmarks.

---

## Split

<a id="p19"></a>
### P19 · Post-split insert failures are ignored

**Type:** Bug · **Severity:** Critical · **Affects:** [§14](./v2-architecture.md#s14)
**Evidence:** `tree.go` → `putRecursive`: when `Split` returns nothing it calls `insertIntoLeaf` and ignores the result; when the left insert fails it falls back to the right page and ignores that result; the right-page insert is also unchecked. `Put` has no error return.
**Trigger:** Any case where the record still does not fit after the split: an oversized record ([P42](#p42)), an existing key ([P22](#p22)), or a record larger than the free space of the half it is routed to.

**What happens.** A mutation that cannot be placed is dropped with no signal to the caller.

**Impact.** Silent data loss.

**v3 resolution.** Every mutation returns an error; a path that cannot place a record returns `ErrPageInvariant` and fails a debug assertion (v3 plan § Mutations).

**Verify.** `FuzzTree` compares every key against a map model after every operation.

<a id="p28"></a>
### P28 · The page's last key is used as its fence

**Type:** Bug · **Severity:** High · **Affects:** [§14](./v2-architecture.md#s14)
**Evidence:** `tree.go` → `putRecursive` and `deleteRecursive` (leaf lock loop) and `slotted_page.go` → `Get` decide "move right" when the key is greater than the page's **last key**, rebuilt with `AssembleFullKey(slotCount − 1)`.
**Trigger:** Left page holds `[a, b]`, right sibling `[m, n]`, parent separator `m`. `Put("c")`: the parent routes to the left page; `c > b` and a sibling exists, so the write moves right and `c` is inserted into the right page, below its separator.

**What happens.** The true upper bound of the left page is the separator (`m`), but v2 uses its current maximum key (`b`). Every key in the gap between them is written to the right sibling.

**Impact.** Pages hold keys outside the range their parent assigns them; left pages stop receiving inserts above their current maximum; every such key costs an extra hop on every lookup; later splits and merges operate on pages that break the separator invariant.

**v3 resolution.** Stored high fence; move right only when `key ≥ highFence` (v3 plan § Split, § Concurrency).

**Verify.** `Check()` on every page after `FuzzTree`: every key satisfies `lowFence ≤ key < highFence` and matches the parent's separators.

<a id="p29"></a>
### P29 · The split midpoint counts tombstones

**Type:** Bug · **Severity:** High · **Affects:** [§14](./v2-architecture.md#s14)
**Evidence:** `slotted_page.go` → `Split` sets `mid = slotCount / 2` over all slots, including tombstones, then drops tombstones while moving and compacting.
**Trigger:** Slots `[a✝, b✝, c✝, d, e, f]` → `mid = 3`, pivot `d` → right page `[d, e, f]`, left page compacts to **0 records**.

**What happens.** Dead slots in the lower half make the split lopsided, down to an empty left page that stays in the tree.

**Impact.** Empty or nearly empty leaves, wasted pages, useless splits.

**v3 resolution.** No tombstones exist; the separator is chosen near the **byte** median of live records (v3 plan § Split).

**Verify.** Every split leaves both halves with at least 25% of the live bytes.

---

## Merge

<a id="p30"></a>
### P30 · Merge ignores the result of locking the right page

**Type:** Bug · **Severity:** Medium (latent) · **Affects:** [§15](./v2-architecture.md#s15)
**Evidence:** `slotted_page.go` → `MergeRight` calls `right.WriteLock()` and ignores its boolean result. `WriteLock` returns false immediately, **without locking**, when the page is obsolete.
**Trigger:** `MergeRight` reached with a right sibling that is already dead (for example through a stale sibling pointer).

**What happens.** The merge reads, copies and marks dead a page it does not hold.

**Impact.** Records copied twice, concurrent writers unguarded, corrupt sibling chain.

**v3 resolution.** Every lock result is checked; an obsolete page aborts the merge (v3 plan § Merge).

**Verify.** Unit test: merge with an obsolete right page returns "not merged" and changes nothing.

<a id="p31"></a>
### P31 · Merge dry run sizes records with the wrong suffix length

**Type:** Bug · **Severity:** Medium (latent) · **Affects:** [§15](./v2-architecture.md#s15)
**Evidence:** `slotted_page.go` → `MergeRight` phase 1 sums `sufLen + valLen` using the **right** page's suffix length; phase 2 inserts the suffix computed against the **left** page's prefix, which is longer when the left prefix is shorter. Phase 2 ignores `InsertRecord` results.
**Trigger:** Left prefix `/a/`, right prefix `/a/b/`, left nearly full. (Unreachable today because prefixes are always empty, [P13](#p13).)

**What happens.** The dry run under-counts; a record can fail to fit mid-migration and is dropped.

**Impact.** Latent data loss once prefixes are enabled.

**v3 resolution.** The dry run re-truncates every key against the merged page's fence-derived prefix before summing (v3 plan § Merge).

**Verify.** Merge tests with differing prefixes; model check after each.

<a id="p32"></a>
### P32 · Scan follows the dead page's back-pointer and returns duplicates

**Type:** Bug · **Severity:** High · **Affects:** [§15](./v2-architecture.md#s15)
**Evidence:** `slotted_page.go` → `MergeRight` points the dead right page's sibling back to the left page. `tree.go` → `Scan` follows sibling pointers without checking the obsolete bit.
**Trigger:** A scan is positioned on the right page when it is merged away; it emits the right page's (still intact) records, follows the pointer back to the left page, and emits those records again.

**What happens.** The back-pointer fixes point lookups but sends scans backwards. Pointing it forward instead would give point lookups false misses.

**Impact.** Duplicate and out-of-order scan results.

**v3 resolution.** A reader that meets an obsolete or recycled page restarts from the root at its last emitted key; no back-pointer (v3 plan § Merge, D8).

**Verify.** Stress test with scans concurrent with merges; results strictly increasing, no duplicates.

<a id="p33"></a>
### P33 · Merge crosses parent boundaries

**Type:** Bug · **Severity:** High (use-after-free in v3) · **Affects:** [§15](./v2-architecture.md#s15)
**Evidence:** `tree.go` → `deleteRecursive` merges a leaf with its right sibling regardless of parent, then searches the current parent's children for the dead page; if absent, nothing is removed.
**Trigger:** Merge the rightmost child of one parent with the leftmost child of the next parent.

**What happens.** The dead page is retired, but its real parent still has a separator and child pointer for it. Readers routed there see a dead page and bounce back through the back-pointer.

**Impact.** Permanent dangling routing entries. Under the Go collector the page stays alive; in an arena that reuses retired pages, this is a use-after-free.

**v3 resolution.** Merge only siblings under the same parent, holding parent, left and right locks in that order (v3 plan § Merge).

**Verify.** After `FuzzTree` with deletes, every child pointer in every inner page resolves to a live page.

<a id="p44"></a>
### P44 · Merge headroom is too small

**Type:** Debt · **Severity:** Medium · **Affects:** [§15](./v2-architecture.md#s15)
**Evidence:** `tree.go` → `deleteRecursive` triggers merge when more than 2,048 bytes are reclaimable; `slotted_page.go` → `MergeRight` accepts any result that leaves 128 bytes free.
**Trigger:** Merge produces a page ~97% full; the next few inserts split it; the next deletes merge it again.

**What happens.** Merge and split thresholds are almost adjacent, so pages oscillate.

**Impact.** Split/merge thrashing: repeated 4 KiB copies and allocations on mixed workloads.

**v3 resolution.** Merge only if the combined live bytes are at most 75% of the page, and run merges lazily in the background (v3 plan § Merge).

**Verify.** Mixed insert/delete benchmark counts splits and merges per 1M operations.

---

## Tree layer

<a id="p34"></a>
### P34 · Internal-node slice swaps can tear

**Type:** Bug · **Severity:** High · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `putRecursive` and `deleteRecursive` assign `n.keys` and `n.children` new slices; `Route` and the read loops copy the slice header without synchronization.
**Trigger:** Concurrent `Get` routing through an internal node while `putRecursive` inserts a separator into it.

**What happens.** A Go slice header is three words (pointer, length, capacity), written non-atomically. A reader can combine the new length with the old pointer, and the bounds check then trusts the wrong length.

**Impact.** Reads beyond the old array, wrong routing, or a crash.

**v3 resolution.** Internal nodes are slotted pages in the arena, read under the same atomic slot and validation rules as leaves (v3 plan § Tree layer).

**Verify.** `-race` build and the concurrent stress test with inner-page splits.

<a id="p35"></a>
### P35 · Loading the root takes a shared lock

**Type:** Debt · **Severity:** Medium · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `Get`, `Put`, `Delete` and `Scan` each call `t.mu.RLock()` to read `t.root`.
**Trigger:** Any multi-core read workload.

**What happens.** Every read-lock acquisition writes the mutex's shared reader count.

**Impact.** The cache line bounces between cores on every operation, the exact cost OLC was chosen to avoid.

**v3 resolution.** Root held in an atomic 64-bit descriptor; readers load it (v3 plan § Tree layer, D10).

**Verify.** Read-only parallel benchmark scales with core count.

<a id="p36"></a>
### P36 · Racing root splits build the wrong tree

**Type:** Bug · **Severity:** High · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `Put` builds the new root from whatever `t.root` holds when it takes the lock, not from the node that actually split.
**Trigger:** Goroutine A splits the root leaf and returns; before A installs a new root, goroutine B splits the same leaf again. A installs `[pA → L, RA]`; B then installs `[pB → (A's root), RB]`, placing leaf `RB` beside an internal node.

**What happens.** Leaves end up at different depths and separators route to the wrong subtree; lookups survive only through move-right detours.

**Impact.** Broken balance invariant, wrong routing, unbounded detours.

**v3 resolution.** The splitting worker compare-and-swaps the root descriptor from the old root to the new one; failure means the root changed, so it restarts from the new root (v3 plan § Tree layer).

**Verify.** Concurrent inserts into an empty tree; afterwards all leaves are at equal depth.

<a id="p41"></a>
### P41 · Tree height never shrinks

**Type:** Debt · **Severity:** Low · **Affects:** [§16](./v2-architecture.md#s16)
**Evidence:** `tree.go` → `deleteRecursive` removes dead children from parents but never removes an internal node with a single child; `Delete` never replaces the root.
**Trigger:** Insert 1M keys, delete them all; the tree keeps its full height with single-child internal nodes.

**What happens.** Deletes shrink leaves but not the upper levels.

**Impact.** Extra levels on every lookup after mass deletes; wasted internal nodes.

**v3 resolution.** **Open.** The v3 plan does not yet collapse single-child roots. Proposed: when the root inner page has no separators, compare-and-swap the root to its only child and retire the old root.

**Verify.** After deleting all keys, tree height returns to 1.

---

## Reclamation and tests

<a id="p38"></a>
### P38 · EBR thread slots wrap at 128

**Type:** Bug · **Severity:** High (fatal in v3) · **Affects:** [§17](./v2-architecture.md#s17)
**Evidence:** `tree.go` → `RegisterThread` returns `counter % MaxThreads` with `MaxThreads = 128` in `ebr.go`. Callers that do not register (all tests and single-threaded benchmarks) pass slot 0.
**Trigger:** Register 129 goroutines: #1 and #129 both get slot 1. #1 calls `Exit` while #129 is still reading.

**What happens.** Two goroutines share one epoch slot; one's exit makes the slot look idle while the other is active, so reclamation can advance past a live reader.

**Impact.** Harmless in v2 (the collector frees memory). In v3, a page can be reused under a reader: silent wrong answers.

**v3 resolution.** `NewWorker()` takes a slot from a free list sized to the machine and returns `ErrTooManyWorkers` when none are left (v3 plan § EBR, D11).

**Verify.** Registering one more worker than there are slots returns an error.

<a id="p39"></a>
### P39 · Benchmarks leak whole trees

**Type:** Bug (test) · **Severity:** High · **Affects:** [§17](./v2-architecture.md#s17)
**Evidence:** `tree_bench_test.go` → the `Put_*` sub-benchmarks call `NewTree()` inside the `b.N` loop and never call `Close()`. `tree.go` → `NewTree` starts a reclamation goroutine with a 50 ms ticker that holds a reference to the tree.
**Trigger:** Run `BenchmarkTree_SlottedPage`.

**What happens.** Every iteration leaves a goroutine running that keeps its entire tree reachable, so the collector can free none of them.

**Impact.** Heap grows with `b.N`; garbage-collection work and wake-ups accumulate; every reported v2 benchmark number is polluted.

**v3 resolution.** Benchmarks close every tree; the reporting rules require allocations, GC pause and RSS alongside time (v3 plan § Testing strategy).

**Verify.** Goroutine count and heap size before and after a benchmark run are equal.

---

## v2 findings that became v3 design ideas

These are improvements rather than flaws; details in the v3 plan's Appendix B.

| # | Idea | Addresses |
|---|---|---|
| D1 | Inline ceiling derived from page geometry | [P42](#p42) |
| D2 | Prefix derived from fence keys | [P13](#p13)–[P15](#p15), [P18](#p18) |
| D3 | Compact before splitting | [P22](#p22) |
| D4 | Key heads in the slot | [P9](#p9) |
| D5 | Split near the byte median | [P29](#p29) |
| D6 | Rightmost split for sequential inserts | 50% fill under sequential load |
| D7 | Shortest separators | Internal fanout |
| D8 | Restart from root on obsolete pages | [P32](#p32) |
| D9 | Merge only at ≤ 75% combined fill | [P44](#p44) |
| D10 | Atomic root | [P35](#p35), [P36](#p36) |
| D11 | Per-worker EBR slots that fail when full | [P38](#p38) |
