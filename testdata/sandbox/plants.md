# Sandbox recall plants

Ten defects planted in `service.go` for the Stage 3 calibration gate (Gate 4).
Each row is one intended finding. Severities are the *expected* bucket; the
live model may rate differently — that is itself calibration signal.

All "secrets" here are fake placeholders, not real credentials.

| # | Line* | Symbol | Expected severity | Category | Defect |
|---|-------|--------|-------------------|----------|--------|
| 1 | `FindUser` | `query :=` | critical | security | SQL built by string concatenation of user input (injection) |
| 2 | `FetchTitle` | `defer resp.Body.Close()` | critical | bug | nil-deref: `resp` is nil on request error but is dereferenced |
| 3 | `CountWords` | `counts[w]++` | critical | bug | concurrent map writes from goroutines (data race) |
| 4 | `LastThree` | `xs[len(xs)-3:]` | major | bug | unchecked slice bound; panics when `len(xs) < 3` |
| 5 | `WriteConfig` | `os.WriteFile(...)` | major | correctness | swallowed error; a failed write looks successful |
| 6 | `adminPassword` | `const adminPassword` | critical | security | hardcoded credential-looking string |
| 7 | `client` | `&http.Client{}` | major | bug | HTTP client has no timeout; can hang forever |
| 8 | `Download` | `io.ReadAll(resp.Body)` | major | security | unbounded response read; memory exhaustion |
| 9 | `Classify` | `else if n > 0` | minor | correctness | dead branch: identical condition, unreachable |
| 10 | `min` | `func min` | nit | style | misleading name: `min` returns the maximum |

\* Anchored by symbol rather than absolute line so the manifest survives edits.

## Severity spread (expected)

- critical: 4 (plants 1, 2, 3, 6)
- major: 4 (plants 4, 5, 7, 8)
- minor: 1 (plant 9)
- nit: 1 (plant 10)

## Fix set for the resolved-flow re-run (Gate 4d)

The re-run fixes **plants 5 and 10** (see `scripts/sandbox_recall.sh fix`):

- Plant 5: propagate the `WriteConfig` error instead of dropping it.
- Plant 10: rename `min` → `max` (correct the misleading name).

After the fix push, sieve must: edit the walkthrough in place, list plants 5
and 10 under **Resolved**, and post **zero** duplicate inline comments for the
still-open plants.
