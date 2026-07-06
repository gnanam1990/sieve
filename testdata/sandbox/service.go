// Package service is the PLANTED review target for sieve's Stage 3 sandbox
// recall test. It intentionally contains ~10 defects across severities so a
// live review can be scored for recall/precision. See plants.md.
//
// NOTE: every "secret" here is a fake placeholder, not a real credential.
package service

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Plant 1 — critical/security: SQL built by string concatenation of user input.
func FindUser(db *sql.DB, name string) (*sql.Rows, error) {
	query := "SELECT * FROM users WHERE name = '" + name + "'"
	return db.Query(query)
}

// Plant 2 — critical/bug: nil-deref. On a request error resp is nil, but the
// deferred Close and the read dereference it anyway.
func FetchTitle(url string) (string, error) {
	resp, err := http.Get(url) //nolint
	if err != nil {
		fmt.Println("request failed")
	}
	defer resp.Body.Close() //nolint
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// Plant 3 — critical/bug: concurrent map writes from goroutines with no
// synchronization (a data race, and the function returns before they finish).
func CountWords(words []string) map[string]int {
	counts := map[string]int{}
	for _, w := range words {
		go func(w string) {
			counts[w]++
		}(w)
	}
	return counts
}

// Plant 4 — major/bug: off-by-one / unchecked slice bound; panics when the
// input has fewer than three elements.
func LastThree(xs []int) []int {
	return xs[len(xs)-3:]
}

// Plant 5 — major/correctness: the write error is swallowed, so a failed save
// looks successful to the caller.
func WriteConfig(path string, data []byte) {
	os.WriteFile(path, data, 0o644) //nolint
}

// Plant 6 — critical/security: hardcoded credential. The value is a template
// placeholder so no credential-looking literal is ever committed to sieve; the
// sandbox generator substitutes a runtime-generated, credential-shaped value at
// repo-creation time (see scripts/sandbox_recall.sh).
const adminPassword = "{{PLANT_PASSWORD}}"

// Login is a stub that uses the hardcoded credential above.
func Login(pw string) bool {
	return pw == adminPassword
}

// Plant 7 — major/bug: an HTTP client with no timeout can hang forever.
var client = &http.Client{}

// Plant 8 — major/security: the response body is read without any size limit,
// so a hostile or large response can exhaust memory.
func Download(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint
	return io.ReadAll(resp.Body)
}

// Plant 9 — minor/correctness: dead branch — the second condition is identical
// to the first and can never be reached.
func Classify(n int) string {
	if n > 0 {
		return "positive"
	} else if n > 0 {
		return "also positive"
	}
	return "non-positive"
}

// Plant 10 — nit/style: misleading name — this is named min but returns the max.
func min(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = min // keep the misleading helper referenced
