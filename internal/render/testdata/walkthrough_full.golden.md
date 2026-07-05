<!-- sieve:walkthrough -->
<!-- sieve:meta v1 eyJ2IjoxLCJoZWFkX3NoYSI6ImhlYWRzaGEwMDExMjIzMzQ0IiwiZnBzIjpbeyJmIjoiZnBpbnRlcm5hbC9kYi9xdWVyeS5nbyIsInAiOiJpbnRlcm5hbC9kYi9xdWVyeS5nbyIsInMiOiJjcml0aWNhbCJ9LHsiZiI6ImZwaW50ZXJuYWwvZ2gvY2xpZW50LmdvIiwicCI6ImludGVybmFsL2doL2NsaWVudC5nbyIsInMiOiJtYWpvciJ9LHsiZiI6ImZwaW50ZXJuYWwvdXRpbC94LmdvIiwicCI6ImludGVybmFsL3V0aWwveC5nbyIsInMiOiJtaW5vciJ9LHsiZiI6ImZwaW50ZXJuYWwvdXRpbC94LmdvIiwicCI6ImludGVybmFsL3V0aWwveC5nbyIsInMiOiJuaXQifSx7ImYiOiJmcGludGVybmFsL2FwaS9oLmdvIiwicCI6ImludGVybmFsL2FwaS9oLmdvIiwicyI6Im1pbm9yIn1dLCJ0cyI6IjIwMjYtMDctMDZUMTI6MDA6MDBaIn0= -->
## sieve review
**2 findings** · 3 notes · 1 resolved · 6 files reviewed, 2 skipped

| Severity | Finding | Where |
|---|---|---|
| 🔴 critical | SQL built by string concatenation | `internal/db/query.go:88` |
| 🟠 major | Unchecked error from Close | `internal/gh/client.go:141` |

<details><summary>📝 Notes (3)</summary>

**`internal/api/h.go`**

- 🟡 minor · Allocation in hot loop (`internal/api/h.go:12`)
  Hoist the buffer out of the loop.

**`internal/util/x.go`**

- 🟡 minor · Prefer errors.Is over == (`internal/util/x.go:5`)
  Comparing errors with == is fragile.

- ⚪ nit · Stutter in name UtilUtil (`internal/util/x.go:30`)
  Rename to avoid stutter.


</details>

<details><summary>✅ Resolved since last review (1)</summary>

- 🟠 major — `internal/old/gone.go`

</details>

<details><summary>⏭️ Skipped files (2)</summary>

- `go.sum` — default exclude
- `docs/x.md` — config exclude: docs/**

</details>

<sub>model `claude-sonnet-5` · tokens in 18.2k / out 1.4k · sieve v0.3.0</sub>
