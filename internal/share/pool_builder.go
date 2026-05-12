package share

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// PassthroughSeed carries one validated passthrough's bootstrap-probe
// result into BuildPoolFromMixed. The cmd layer issues the probe and
// constructs these; pool_builder does not perform HTTP.
type PassthroughSeed struct {
	Ticket        Ticket
	Feasibility   *float64 // nil iff Unconstrained
	Unconstrained bool
	Degraded      bool // probe returned 503
}

// BuildPool is the legacy entry point that callers used before
// passthrough support. Equivalent to BuildPoolFromMixed with no
// passthrough seeds.
func BuildPool(args []string, prompt string, skipCapture bool) (*credPool, *store.Credential, error) {
	pool, initialCred, _, err := BuildPoolFromMixed(args, nil, prompt, skipCapture)
	return pool, initialCred, err
}

// BuildPoolFromMixed builds a credPool from a mix of local-cred
// resolver args and pre-validated passthrough seeds. localArgs is
// the resolver input list (id, prefix, or name); empty slice means
// "no local creds". passthroughs is the slice produced by the
// cmd-layer bootstrap probe.
//
// Returns: the pool, the *store.Credential to capture (nil for
// passthrough-only pool), and the initial-activated *poolEntry.
// The initial-activated may be either a local-cred entry or a
// passthrough entry, depending on feasibility ranking.
func BuildPoolFromMixed(
	localArgs []string,
	passthroughs []PassthroughSeed,
	prompt string,
	skipCapture bool,
) (*credPool, *store.Credential, *poolEntry, error) {
	resolved, err := resolvePoolArgs(localArgs)
	if err != nil {
		return nil, nil, nil, err
	}

	type admittedEntry struct {
		entry       *poolEntry
		cred        *store.Credential // nil for passthrough
		feasibility float64
	}
	type passAEntry struct {
		state *credState
		cred  *store.Credential
	}

	pool := &credPool{entries: make(map[string]*poolEntry)}
	var rejections []string
	var passA []passAEntry

	// Pass A: local-cred token-validity admission.
	for _, cred := range resolved {
		state, serr := newCredState(cred)
		if serr != nil {
			msg := fmt.Sprintf("%s(%s): %v", credLogName(cred), shortID(cred.ID), serr)
			rejections = append(rejections, msg)
			fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
			continue
		}
		if _, ferr := state.Fresh(); ferr != nil {
			msg := fmt.Sprintf("%s(%s): refresh failed: %v", credLogName(cred), shortID(cred.ID), ferr)
			rejections = append(rejections, msg)
			fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
			continue
		}
		passA = append(passA, passAEntry{state: state, cred: cred})
	}

	var admitted []admittedEntry

	// Pass B (local usage probe): skipped for singleton local-cred
	// pools (no rotation possible). Run only when there is more than
	// one entry total — locals plus passthroughs.
	totalCount := len(passA) + len(passthroughs)
	if totalCount > 1 {
		for _, pa := range passA {
			info := oauth.FetchUsageFn(pa.cred.ClaudeAiOauth.AccessToken)
			if info == nil || info.Error != "" {
				reason := "usage probe returned nil"
				if info != nil {
					reason = info.Error
				}
				msg := fmt.Sprintf("%s(%s): %s", credLogName(pa.cred), shortID(pa.cred.ID), reason)
				rejections = append(rejections, msg)
				fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
				continue
			}
			f := computeFeasibility(info, timeNow())
			admitted = append(admitted, admittedEntry{
				entry: &poolEntry{
					state:           pa.state,
					status:          statusCandidate,
					lastUsage:       info,
					lastUsageAt:     timeNow(),
					lastFeasibility: f,
				},
				cred:        pa.cred,
				feasibility: f,
			})
		}
	} else if len(passA) == 1 && len(passthroughs) == 0 {
		// True singleton: skip the probe.
		pa := passA[0]
		admitted = append(admitted, admittedEntry{
			entry: &poolEntry{
				state:           pa.state,
				status:          statusCandidate,
				lastUsage:       nil,
				lastUsageAt:     time.Time{},
				lastFeasibility: 0,
			},
			cred:        pa.cred,
			feasibility: 0,
		})
	}

	// Pass A': passthrough seeding. Probes already ran at cmd-level;
	// seeds carry the result.
	for _, seed := range passthroughs {
		pt := newPassthroughEntryState(seed.Ticket)
		var override float64
		if seed.Unconstrained {
			override = math.MaxFloat64
		} else if seed.Feasibility != nil {
			override = *seed.Feasibility
		}
		entry := &poolEntry{
			state:               pt,
			status:              statusCandidate,
			feasibilityOverride: &override,
			lastUsageAt:         timeNow(),
			lastFeasibility:     override,
		}
		if seed.Degraded {
			entry.status = statusDegraded
		}
		admitted = append(admitted, admittedEntry{
			entry:       entry,
			cred:        nil,
			feasibility: override,
		})
	}

	// Sort admitted entries: highest feasibility first; ID lex tie-break.
	sort.Slice(admitted, func(i, j int) bool {
		if admitted[i].feasibility != admitted[j].feasibility {
			return admitted[i].feasibility > admitted[j].feasibility
		}
		idI := admitted[i].entry.state.credID()
		idJ := admitted[j].entry.state.credID()
		return idI < idJ
	})

	// Capture loop: pick the highest-feasibility LOCAL cred for
	// capture (its headers are stored on its own entry). If no local
	// cred is admitted, skip capture entirely.
	var captureRejects []string
	captureFailedIDs := make(map[string]bool)

	for _, ad := range admitted {
		if ad.cred == nil {
			continue // passthrough
		}
		if !skipCapture {
			h, cerr := captureCredFn(ad.cred, prompt)
			if cerr != nil {
				msg := fmt.Sprintf("%s(%s): capture failed: %v", credLogName(ad.cred), shortID(ad.cred.ID), cerr)
				captureRejects = append(captureRejects, msg)
				captureFailedIDs[ad.cred.ID] = true
				fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
				continue
			}
			ad.entry.captured = h
		}
		break
	}

	// Build the pool: admit every entry that hasn't been
	// capture-rejected. The initial activated is the highest-
	// feasibility entry overall (admitted[0]) — could be a passthrough
	// even if a local cred was captured.
	for _, ad := range admitted {
		if ad.cred != nil && captureFailedIDs[ad.cred.ID] {
			continue
		}
		pool.entries[ad.entry.state.credID()] = ad.entry
	}
	if len(pool.entries) == 0 {
		// All local creds failed capture, AND no passthrough seeds.
		if len(captureRejects) > 0 {
			return nil, nil, nil, fmt.Errorf("ccm: no candidate could be captured:\n  %s",
				joinLines(captureRejects))
		}
		if len(rejections) > 0 {
			return nil, nil, nil, fmt.Errorf("ccm: no usable credentials in pool:\n  %s",
				joinLines(rejections))
		}
		return nil, nil, nil, errors.New("ccm: no credentials in store; run `ccm login` first")
	}

	// Pick the initial activated: walk admitted in order; the first
	// one still in pool.entries is the winner. The captured local-
	// cred headers sit dormant on their owning entry's
	// poolEntry.captured field; the director's activatedView only
	// reads them when that entry is the currently-activated.
	var initialEntry *poolEntry
	var initialCred *store.Credential
	for _, ad := range admitted {
		if _, ok := pool.entries[ad.entry.state.credID()]; !ok {
			continue
		}
		initialEntry = ad.entry
		initialCred = ad.cred
		break
	}
	// Only promote to statusActivated when the entry is healthy;
	// a degraded initial entry keeps its statusDegraded so the
	// director's errNoActivated path fires correctly.
	if initialEntry.status != statusDegraded {
		initialEntry.status = statusActivated
	}
	pool.activated = initialEntry.state.credID()
	pool.singleton = len(pool.entries) == 1

	fmt.Fprintf(errLog(), "ccm: load-balance pool: %d candidates, initial activated %s (lifetime %s)\n",
		len(pool.entries), initialEntry.state.credName(), formatLifetime(initialEntry.lastFeasibility))

	return pool, initialCred, initialEntry, nil
}

// resolvePoolArgs turns CLI arguments into a deduped list of
// *store.Credential. Empty args = every credential in the store.
func resolvePoolArgs(args []string) ([]*store.Credential, error) {
	if len(args) == 0 {
		all, err := store.List()
		if err != nil {
			return nil, err
		}
		var out []*store.Credential
		for _, c := range all {
			if c.ProviderName() != "claude" {
				fmt.Fprintf(os.Stderr, "ccm: skipping %s (provider=%s; share/launch pool is claude-only)\n", c.Name, c.ProviderName())
				continue
			}
			out = append(out, c)
		}
		return out, nil
	}
	seen := make(map[string]struct{})
	var out []*store.Credential
	for _, a := range args {
		c, err := store.Resolve(a)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", a, err)
		}
		if c.ProviderName() != "claude" {
			return nil, fmt.Errorf("share/launch pool is claude-only; %s is %s", c.Name, c.ProviderName())
		}
		if _, ok := seen[c.ID]; ok {
			continue
		}
		seen[c.ID] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

func credLogName(c *store.Credential) string {
	if c.Name != "" {
		return c.Name
	}
	return shortID(c.ID)
}

func joinLines(lines []string) string {
	out := ""
	for i, s := range lines {
		if i > 0 {
			out += "\n  "
		}
		out += s
	}
	return out
}

// timeNow is a test seam over time.Now to keep BuildPool's
// determinism in step with the rest of the package.
var timeNow = func() time.Time { return time.Now() }
