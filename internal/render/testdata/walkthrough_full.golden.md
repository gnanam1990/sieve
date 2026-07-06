<!-- sieve:walkthrough -->
<!-- sieve:meta v2 eyJ2IjoyLCJoZWFkX3NoYSI6ImhlYWRzaGEwMDExMjIzMzQ0IiwidHMiOiIyMDI2LTA3LTA2VDEyOjAwOjAwWiIsImZpbmRpbmdzIjpbeyJmIjoiZnBpbnRlcm5hbC9kYi9xdWVyeS5nbyIsInAiOiJpbnRlcm5hbC9kYi9xdWVyeS5nbyIsImwiOjg4LCJzZCI6IlIiLCJzIjoiY3JpdGljYWwiLCJjIjowLjk1LCJ0IjoiU1FMIGJ1aWx0IGJ5IHN0cmluZyBjb25jYXRlbmF0aW9uIiwiY2lkIjowLCJjYXQiOiJzZWN1cml0eSIsInRyIjoiaW5saW5lIn0seyJmIjoiZnBpbnRlcm5hbC9naC9jbGllbnQuZ28iLCJwIjoiaW50ZXJuYWwvZ2gvY2xpZW50LmdvIiwibCI6MTQxLCJzZCI6IlIiLCJzIjoibWFqb3IiLCJjIjowLjg2LCJ0IjoiVW5jaGVja2VkIGVycm9yIGZyb20gQ2xvc2UiLCJjaWQiOjAsImNhdCI6ImJ1ZyIsInRyIjoiaW5saW5lIn0seyJmIjoiZnBpbnRlcm5hbC91dGlsL3guZ28iLCJwIjoiaW50ZXJuYWwvdXRpbC94LmdvIiwibCI6NSwic2QiOiJSIiwicyI6Im1pbm9yIiwiYyI6MC43MiwidCI6IlByZWZlciBlcnJvcnMuSXMgb3ZlciA9PSIsImNpZCI6MCwiY2F0Ijoic3R5bGUiLCJ0ciI6Im5vdGVzIn0seyJmIjoiZnBpbnRlcm5hbC91dGlsL3guZ28iLCJwIjoiaW50ZXJuYWwvdXRpbC94LmdvIiwibCI6MzAsInNkIjoiUiIsInMiOiJuaXQiLCJjIjowLjY1LCJ0IjoiU3R1dHRlciBpbiBuYW1lIFV0aWxVdGlsIiwiY2lkIjowLCJjYXQiOiJzdHlsZSIsInRyIjoibm90ZXMifSx7ImYiOiJmcGludGVybmFsL2FwaS9oLmdvIiwicCI6ImludGVybmFsL2FwaS9oLmdvIiwibCI6MTIsInNkIjoiUiIsInMiOiJtaW5vciIsImMiOjAuNjgsInQiOiJBbGxvY2F0aW9uIGluIGhvdCBsb29wIiwiY2lkIjowLCJjYXQiOiJwZXJmIiwidHIiOiJub3RlcyJ9XSwicmVzb2x2ZWQiOlsiZGVhZGJlZWZkZWFkYmVlZiJdfQ== -->
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
