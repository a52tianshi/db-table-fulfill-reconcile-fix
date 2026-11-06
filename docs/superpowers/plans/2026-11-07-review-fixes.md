# Review fixes implementation plan

Goal: fix the four reproduced implementation defects and reject misrouted physical rows; preserve single-threaded batching and single-row UPDATE. User authorized implementation and explicitly prohibited commit/push.

Architecture: preserve Run/Source/Repairer. Count a committed batch before calling Notifier. Use unsigned magnitude for int64 tolerance and reject negative tolerance. Reject promoted Struct config names. Require Schema.KeyLess to order primary keys before splitting locking IN queries. Verify each scanned/fetched row routes to its physical table and expose ErrMisrouted.

Spec: docs/reviews/2026-11-07/README.md and user approval.

Constraints: Go 1.22, standard-library root, no batch UPDATE, never delete Extra, no commit/push, leave audit evidence intact. Work in the user-selected repository; temporary MySQL instances only.

Review focus: all committed rows counted even if Notify fails; negative/extreme tolerance; embedded pointer fields rejected before comparison; numeric key 2/10 sorted numerically across IN chunks; wrong-shard extra/duplicate rows must stop rather than report consistent.

- [x] Add regression tests for comparator bounds/promoted fields, committed finding accounting, lock chunk order and required KeyLess, misrouted scan/fetch; run against baseline and observe failures.
- [x] Fix Within/Struct and whole-batch accounting; run focused root tests.
- [x] Implement required KeyLess, ordered locking chunks, ErrMisrouted and routing checks; update all constructors/examples/E2E mappings; run focused adapter tests.
- [x] Add real MySQL regression tests for ascending cross-chunk locks, correct extras and wrong-shard rows, high-water behavior; bound lock-test waits.
- [x] Update README/interface contracts/Schema docs, notification semantics, high-water/snapshot and memory caveats. Convert historical observation tests to current-behavior regressions while preserving historical logs.
- [x] Run race/vet/repeats, tolerance fuzz, 2200-case matrix, isolated MySQL integration and three insert-mode E2E; independent code review; record results without committing.

Execution notes: regression tests first failed on the baseline for all five targeted behaviors. Focused and full suites are green. KeyLess is an intentional required API addition (all local examples/store mappings updated). Whole-batch Warned is a policy count, not delivery confirmation. Historical audit tests/logs preserved; current independent repro module now asserts fixed behavior. A fresh reviewer found no remaining implementation defect; its non-blocking Lock routing rollback coverage suggestion was added as a real MySQL test.

Final verification: root race repeated 10x; 1,027,545 tolerance fuzz executions; 2200-case matrix; 11 MySQL top-level tests repeated 3x with no Skip; all three 100-shard E2E modes independently verified; no commit/push. Detailed outcomes: docs/reviews/2026-11-07/fixes.md.

Repository packaging: docs/reviews is intentionally ignored by the user; referenced audit material and raw logs are local-only. Public behavior and verification summary are documented in README.md and docs/interface-contracts.md.
