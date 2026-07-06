package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/gnanam1990/sieve/internal/gate"
	"github.com/gnanam1990/sieve/internal/gh"
	"github.com/gnanam1990/sieve/internal/memory"
	"github.com/gnanam1990/sieve/internal/post"
	"github.com/gnanam1990/sieve/internal/render"
)

// Sync rebuilds the local outcome store for a PR from GitHub alone: the
// walkthrough's active findings (metadata), sieve's inline comments (posted
// findings, categories/confidences, reaction snapshots), and the PR's resolved
// review threads (dismissals). It Rewrites the store, so re-running is
// idempotent (dedupe by natural key is inherent — one event per fingerprint).
//
// Reconstruction notes: run events (token history) are live-only telemetry and
// are not reconstructable from GitHub, so sync omits them. A posted finding no
// longer active is treated as resolved-via-anchor-gone (a fix) — the common
// case; the live "re-review-absent" nuance (the model changed its mind on a
// re-reviewed file) is not distinguishable from GitHub. Learnings clusters and
// reaction/dismissal aggregates reconstruct exactly.
func Sync(ctx context.Context, opts Options) (int, error) {
	owner, name, ok := strings.Cut(opts.Repo, "/")
	if !ok || owner == "" || name == "" {
		return 0, fmt.Errorf("invalid --repo %q, want owner/name", opts.Repo)
	}
	client, err := gh.New(gh.NewStaticTokenSource(opts.Token), opts.Log)
	if err != nil {
		return 0, err
	}
	if opts.APIBaseURL != "" {
		client.BaseURL = opts.APIBaseURL
	}
	poster := &post.Poster{Client: client, Owner: owner, Repo: name, PR: opts.PRNumber, Log: opts.Log}

	loc, err := poster.LocateWalkthrough(ctx)
	if err != nil {
		return 0, fmt.Errorf("locate walkthrough: %w", err)
	}
	comments, err := poster.Comments(ctx)
	if err != nil {
		return 0, fmt.Errorf("list review comments: %w", err)
	}
	threads, err := poster.ResolvedThreads(ctx)
	if err != nil {
		return 0, fmt.Errorf("fetch review threads: %w", err)
	}

	activeByFp := make(map[string]gate.CompactFinding)
	for _, cf := range loc.Meta.Findings {
		activeByFp[cf.Fp] = cf
	}
	resolvedThreadFp := make(map[string]bool)
	for _, th := range threads {
		if !th.IsResolved {
			continue
		}
		for _, cm := range th.Comments {
			if fp := render.ParseFpMarker(cm.Body); fp != "" {
				resolvedThreadFp[fp] = true
			}
		}
	}

	ts := opts.now()
	var events []memory.Event
	posted := make(map[string]bool)

	for _, c := range comments {
		fp, cat, conf, isSieve := render.ParseInline(c.Body)
		if !isSieve {
			continue
		}
		posted[fp] = true
		e := memory.Event{Ts: ts, Type: memory.TypeFinding, Fp: fp, Cat: cat, Conf: conf, Tier: "inline", Cid: c.ID, Path: c.Path}
		if cf, active := activeByFp[fp]; active {
			e.Path, e.Sev, e.Title = cf.Path, cf.Severity, cf.Title
			if cf.Category != "" {
				e.Cat = cf.Category
			}
			if cf.Conf != 0 {
				e.Conf = cf.Conf
			}
		}
		events = append(events, e)

		if c.Reactions.PlusOne != 0 || c.Reactions.MinusOne != 0 {
			events = append(events, memory.Event{Ts: ts, Type: memory.TypeReaction, Fp: fp, Cid: c.ID, Plus: c.Reactions.PlusOne, Minus: c.Reactions.MinusOne})
		}
		if _, active := activeByFp[fp]; !active {
			events = append(events, memory.Event{Ts: ts, Type: memory.TypeResolved, Fp: fp, Path: c.Path, How: memory.ResolvedAnchorGone})
		}
	}

	// Active notes-tier findings have no inline comment; recover them from meta.
	for _, cf := range loc.Meta.Findings {
		if cf.Tier == "notes" && !posted[cf.Fp] {
			events = append(events, memory.Event{Ts: ts, Type: memory.TypeFinding, Fp: cf.Fp, Path: cf.Path, Sev: cf.Severity, Conf: cf.Conf, Cat: cf.Category, Tier: "notes", Title: cf.Title})
		}
	}
	// Dismissals: an active finding whose thread is resolved (closed without a fix).
	for fp := range resolvedThreadFp {
		if _, active := activeByFp[fp]; active {
			events = append(events, memory.Event{Ts: ts, Type: memory.TypeDismissed, Fp: fp})
		}
	}

	store := memory.Open(memoryHost, owner, name, opts.Log)
	store.Rewrite(events)
	return len(events), nil
}
