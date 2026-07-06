// Package webhook is the daemon's HTTP front door: it verifies GitHub webhook
// signatures (constant-time, before any parsing), dedupes redeliveries, and
// turns pull_request events into review jobs. It exposes /webhook and /healthz
// and nothing else.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/gnanam1990/sieve/internal/queue"
)

// maxBody caps a webhook payload; GitHub's are well under 1 MB.
const maxBody = 1 << 20

// enqueueActions are the pull_request actions worth a (re)review.
var enqueueActions = map[string]bool{
	"opened": true, "synchronize": true, "reopened": true, "ready_for_review": true,
}

// Config wires a Handler to the daemon.
type Config struct {
	Secret       []byte   // webhook HMAC secret; required
	ReposAllow   []string // globs; empty = all installed repos
	ReviewDrafts bool     // server-side draft policy
	Enqueue      func(queue.Job) error
	QueueStats   func() (depth, dead int)
	Version      string
	Log          *slog.Logger
}

// Handler serves /webhook and /healthz.
type Handler struct {
	cfg      Config
	dedupe   *deliveryLog
	rejected atomic.Int64 // signature verification failures
}

// New builds a Handler, loading the persisted delivery log from dataDir.
func New(cfg Config, dataDir string) (*Handler, error) {
	if len(cfg.Secret) == 0 {
		return nil, fmt.Errorf("webhook: secret is required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	d, err := openDeliveryLog(dataDir, 4096)
	if err != nil {
		return nil, err
	}
	return &Handler{cfg: cfg, dedupe: d}, nil
}

// Mux returns the HTTP routes; nothing beyond /webhook and /healthz is exposed.
func (h *Handler) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", h.handleWebhook)
	mux.HandleFunc("/healthz", h.handleHealthz)
	return mux
}

// RejectedCount is the number of deliveries that failed signature verification.
func (h *Handler) RejectedCount() int64 { return h.rejected.Load() }

// Close flushes and closes the delivery-dedupe log.
func (h *Handler) Close() error { return h.dedupe.close() }

func (h *Handler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// http.MaxBytesReader both bounds the read AND marks the connection for
	// close on overflow, so a client cannot stream an oversized body past the
	// limit and keep the keep-alive socket open (a slowloris-style drain).
	r.Body = http.MaxBytesReader(w, r.Body, maxBody+1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	// Verify the signature (constant-time) BEFORE parsing anything. On failure,
	// count it and reply 401 — never log the body. Only the rejection count is
	// kept; no header values are logged on the failure path so a guessed GUID
	// or event name does not learn whether that delivery was processed.
	if !verifySignature(h.cfg.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		h.rejected.Add(1)
		h.cfg.Log.Warn("webhook signature verification failed")
		http.Error(w, "signature mismatch", http.StatusUnauthorized)
		return
	}

	delivery := r.Header.Get("X-GitHub-Delivery")
	if h.dedupe.has(delivery) {
		h.cfg.Log.Debug("dropping redelivered webhook", "delivery", delivery)
		w.WriteHeader(http.StatusOK)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	switch event {
	case "ping":
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
	case "installation", "installation_repositories":
		h.logInstallation(event, body)
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
	case "pull_request":
		h.handlePullRequest(w, delivery, body)
	default:
		// Unknown events must never error — ack and ignore.
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) handlePullRequest(w http.ResponseWriter, delivery string, body []byte) {
	var ev struct {
		Action      string `json:"action"`
		Number      int    `json:"number"`
		PullRequest struct {
			Draft bool `json:"draft"`
			Head  struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if !enqueueActions[ev.Action] {
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
		return
	}
	if !h.repoAllowed(ev.Repository.FullName) {
		h.cfg.Log.Info("skipping repo outside allowlist", "repo", sanitize(ev.Repository.FullName))
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
		return
	}
	if ev.PullRequest.Draft && !h.cfg.ReviewDrafts {
		h.cfg.Log.Debug("skipping draft PR (review_drafts off)", "repo", sanitize(ev.Repository.FullName), "pr", ev.Number)
		h.record(delivery)
		w.WriteHeader(http.StatusOK)
		return
	}
	jobRec := queue.Job{
		InstallationID: ev.Installation.ID,
		Repo:           sanitize(ev.Repository.FullName),
		PR:             ev.Number,
		HeadSHA:        ev.PullRequest.Head.SHA,
		Draft:          ev.PullRequest.Draft,
		DeliveryID:     delivery,
	}
	if err := h.cfg.Enqueue(jobRec); err != nil {
		// Do NOT record the delivery — a 5xx makes GitHub redeliver so the job
		// isn't lost to a transient enqueue/log failure.
		h.cfg.Log.Error("enqueue failed", "repo", jobRec.Repo, "pr", jobRec.PR, "err", err)
		http.Error(w, "enqueue failed", http.StatusServiceUnavailable)
		return
	}
	h.record(delivery)
	h.cfg.Log.Info("enqueued review", "repo", jobRec.Repo, "pr", jobRec.PR, "action", ev.Action)
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) logInstallation(event string, body []byte) {
	var ev struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installation"`
	}
	_ = json.Unmarshal(body, &ev)
	h.cfg.Log.Info("installation event", "event", event, "action", ev.Action, "installation", ev.Installation.ID, "account", sanitize(ev.Installation.Account.Login))
}

// repoAllowed reports whether full ("owner/name") matches the allowlist. An
// empty allowlist permits every installed repo.
func (h *Handler) repoAllowed(full string) bool {
	if len(h.cfg.ReposAllow) == 0 {
		return true
	}
	for _, g := range h.cfg.ReposAllow {
		if ok, _ := doublestar.Match(g, full); ok {
			return true
		}
	}
	return false
}

// record marks a delivery processed (dedupe + persistence). Empty GUIDs (absent
// header) are not recorded — nothing to dedupe against.
func (h *Handler) record(delivery string) {
	if delivery == "" {
		return
	}
	if err := h.dedupe.record(delivery); err != nil {
		h.cfg.Log.Warn("could not persist delivery id", "delivery", delivery, "err", err)
	}
}

func (h *Handler) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	depth, dead := 0, 0
	if h.cfg.QueueStats != nil {
		depth, dead = h.cfg.QueueStats()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            "ok",
		"version":           h.cfg.Version,
		"queue_depth":       depth,
		"dead_letters":      dead,
		"rejected_webhooks": h.rejected.Load(),
	})
}

// sanitize replaces any character a slog text-handler would pass through verbatim
// and that could split a log line / mislead a SIEM (CR, LF, TAB, and other
// control chars), so a malicious GitHub payload field cannot log-forge its way
// onto an "enqueued review"/"job failed" line. GitHub.com already constrains
// repo/account names, but the threat model is GitHub Enterprise Server / a
// third-party App gateway where those constraints do not hold.
func sanitize(s string) string {
	if !strings.ContainsAny(s, "\r\n\t\x00") {
		return s
	}
	b := make([]byte, len(s))
	for i, c := range []byte(s) {
		switch c {
		case '\r', '\n', '\t', 0:
			b[i] = '?'
		default:
			b[i] = c
		}
	}
	return string(b)
}

// verifySignature checks the sha256 HMAC of body against the X-Hub-Signature-256
// header using a constant-time compare. A malformed or absent header is a
// failure, not a bypass.
func verifySignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}
