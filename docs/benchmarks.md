# Browser benchmarks

B-18 measures the two properties that make stable refs useful to an agent:
ref retention after an interaction and the size of the follow-up snapshot when the
agent requests a diff.

## Reproduce

```text
CGO_ENABLED=0 go test -count=1 -v ./internal/engine -run TestStableRefsAndSnapshotDiffBenchmark
```

The test starts the in-process B-15 fixture server and checks every registered
fixture route except `/spa` (the delayed-hydration fixture is intentionally
excluded because its DOM is expected to change while it hydrates). It then feeds
a deterministic accessibility-tree representation of each fixture into the
protocol-neutral `internal/engine` service, clicks the fixture action through the
interaction service, and captures the follow-up snapshot both ways:

- **no diff:** serialized `SnapshotResult`;
- **diff:** serialized `SnapshotDiffResult` from `SnapshotDiff`.

The token column is a renderer-level estimate, not a provider-specific tokenizer:
`ceil(UTF-8 rune count of the compact JSON payload / 4)`. Keeping the estimator
inside the CGO-free test makes the threshold repeatable in CI and avoids claiming
measurements from a model tokenizer that is not part of this repository.

## Latest deterministic run

The following table is emitted by the test and was captured with the command
above on the B-18 worktree:

| Fixture | Initial refs | Retained refs | Retention | Follow-up without `--diff` (tokens) | Follow-up with `--diff` (tokens) |
|---|---:|---:|---:|---:|---:|
| `aria-label-mismatch` | 6 | 5 | 83.3% | 401 | 136 |
| `form` | 6 | 5 | 83.3% | 386 | 136 |
| `hidden-text` | 6 | 5 | 83.3% | 393 | 136 |
| `iframe` | 6 | 5 | 83.3% | 388 | 136 |
| `internal-server-error` | 6 | 5 | 83.3% | 401 | 136 |
| `not-found` | 6 | 5 | 83.3% | 391 | 136 |
| `overlay` | 6 | 5 | 83.3% | 389 | 136 |
| `redirect-loop` | 6 | 5 | 83.3% | 395 | 136 |
| `shadow-dom` | 6 | 5 | 83.3% | 392 | 136 |
| `slow` | 6 | 5 | 83.3% | 386 | 136 |
| `static` | 6 | 5 | 83.3% | 388 | 136 |
| **median** | — | — | — | — | **136** |

## Gates and residual limitation

The test fails when any covered fixture retains less than 80% of its refs or when
the median diff payload is 200 tokens or more. The current deterministic result
therefore passes both B-18 gates: every fixture retains 83.3% and the median diff
cost is 136 tokens.

This is a renderer/service benchmark, not a real-Chrome benchmark. It verifies
stable ref allocation, interaction-to-snapshot state flow, diff classification,
and payload sizing without requiring Chrome or CGO. It does not measure CDP
latency, Chrome's accessibility-tree generation, or a particular LLM tokenizer.
A future Chrome-backed benchmark can reuse the same result schema and thresholds
when a hermetic browser is available in CI.

## Prompt injection scan benchmark

Issue #192 measures the execution time and allocation overhead of `injection.Scan` over a large HTML fixture (~100 KB, ~500 DOM elements) before and after compiling the pattern list into a single compiled matcher and caching the parsed pattern sets.

### Reproduce

```text
CGO_ENABLED=0 go test -bench=BenchmarkScan -benchmem -count=1 ./internal/injection
```

### Measurements

| Implementation | Latency (ns/op) | Memory (B/op) | Allocations (allocs/op) |
|---|---:|---:|---:|
| Before (linear pattern loop, parsed per scan) | ~4,700,000 ns/op (4.70 ms) | 1,783,724 B/op (1.78 MB) | 89,092 allocs/op |
| After (compiled-once cached matcher) | ~4,140,000 ns/op (4.14 ms) | 1,767,932 B/op (1.77 MB) | 88,871 allocs/op |

The compiled pattern matcher evaluates all candidate injection phrases in a single traversal pass while avoiding redundant pattern file reading and parsing across subsequent scan invocations.

