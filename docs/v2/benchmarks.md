# v2 Benchmark Baseline

## Environment
- CPU: i5-1235U (2P + 8E), pinned to P-core 0 for single-threaded, all cores for parallel
- OS: Linux, Go 1.22
- Power profile: performance
- 10 runs per benchmark, benchstat for statistics

## Method
- Correctness guards verify every key after every load (see tree_bench_test.go)
- Race detector run separately (see race-evidence/)
- Delete benchmark rebuilds the tree every N deletions

## Results
 see [`docs/v2/benchstat.txt`](benchstat.txt)

## Claims vs. Measured (1M keys, i5-1235U P-core, 10 runs)

| Benchmark | Published (X/LinkedIn) | Measured | Verdict |
|---|---|---|---|
| Put_Sequential | 916 ms / 372,987 allocs | 312 ms / 84,368 allocs | Code changed after post (COW leak fix). Retract old number. |
| Put_UpdateHeavy | 650 ms / 223,069 allocs | 194 ms / 906,590 allocs | Latency 3× better; allocs 4× worse. Both numbers changed. |
| Delete | 506 ns / **0 allocs** | 322 ns / 3 allocs | **"Zero-alloc" is false.** |
| Scan (100 keys) | 31,647 ns / 220 allocs | 13.9 µs / 335 allocs | Latency better; allocs higher. |
| Get_Hot | not published | 165 ns / 1 alloc | Every read allocates. |
| Parallel Put 1M | 215 ns / **0 allocs** | 185 ns / 1 alloc | **"Zero-alloc" is false.** |
| Mixed 1M | 346 ns / 4 allocs | 288 ns / 9 allocs | Latency holds; allocs doubled. |
| "Scales clean" | claimed | FAIL: race detector (all 4 parallel), correctness guard (key-order corruption 1/10 runs) | **Retract.** |

### Notes

- "Published" numbers are from the Sep 27 X thread and LinkedIn post.
- "Measured" numbers are the P-core-pinned, guarded, 10-run baseline in `bench-raw.txt`.
- Put_Sequential and Put_UpdateHeavy improved because commit `c968ec2` (COW leak fix) landed between the posts and this rerun. The posted numbers were measured on pre-fix code.
- The scan slowdown was attributed to "traversing prefix-compressed 4KB page boundaries." Prefix compression was never activated by the tree (P13). The real costs are two allocations per key (P16/P17) and walking the first leaf from slot 0 (P24).
- "0 allocs/op" on parallel put was measured on a workload where most operations are same-length in-place updates, not inserts. The one allocation is the `[]byte` return from `Get` inside the OLC retry path.
- The corruption evidence is in `docs/v2/bench-run2-corruption.txt` line 274–275; the race evidence is in `docs/v2/race-evidence/`.

## Correctness
- All sequential guards: PASS
- Race detector: FAIL (all 4 parallel benchmarks, 205 warnings, 6 distinct conflicts)
- Correctness guard: FAIL (key-order corruption in MixedWorkload, 1/10 runs)
- Details: v2-known-issues.md § P26, P45, P46