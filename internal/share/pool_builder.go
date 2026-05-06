package share

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/oauth"
	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// BuildPool runs the startup filter for --load-balance mode and
// returns a credPool plus the *store.Credential that should be
// passed to the capture phase (the initial activated entry).
//
// args is the list of resolver inputs (ID, prefix, or name) from
// the CLI; an empty slice means "every credential in the store".
// Whitespace-equivalent duplicates are deduped after resolution.
//
// Capture model: BuildPool runs captureCredFn on the highest-
// feasibility admitted cred. On capture success, that cred becomes
// the initial activated; remaining admitted creds enter the pool as
// candidates with captured=nil (the scheduler captures them at
// promotion time). On capture failure, BuildPool falls through to
// the next-best cred (implicit pool) or returns a fatal error
// (explicit pool).
//
// When skipCapture is true (launch --load-balance mode), the capture
// loop short-circuits: the highest-feasibility admitted cred becomes
// the initial activated with captured=nil, and the remaining
// admitted creds enter as candidates with captured=nil. The spawned
// claude provides its own outbound headers via LocalProxy, so per-
// cred capture is unnecessary.
func BuildPool(args []string, prompt string, skipCapture bool) (*credPool, *store.Credential, error) {
	resolved, err := resolvePoolArgs(args)
	if err != nil {
		return nil, nil, err
	}

	type admittedEntry struct {
		entry       *poolEntry
		cred        *store.Credential
		feasibility float64
	}
	type passAEntry struct {
		state *credState
		cred  *store.Credential
	}

	pool := &credPool{entries: make(map[string]*poolEntry)}
	var rejections []string
	var passA []passAEntry

	// Pass A: token-validity admission. Refresh each cred; reject
	// only on refresh failure. NO usage probe at this stage — the
	// usage endpoint is rate-limited by Anthropic, so we want to
	// avoid calling it for the singleton path entirely.
	for _, cred := range resolved {
		state := newCredState(cred)
		if _, ferr := state.Fresh(); ferr != nil {
			msg := fmt.Sprintf("%s(%s): refresh failed: %v", credLogName(cred), shortID(cred.ID), ferr)
			rejections = append(rejections, msg)
			fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
			continue
		}
		passA = append(passA, passAEntry{state: state, cred: cred})
	}

	var admitted []admittedEntry

	// Pass B: usage probe — only when more than one cred passed Pass A.
	// A singleton pool can't rotate, so the probe would be wasted
	// API spend. The lone cred enters with lastUsage=nil; the
	// scheduler's singleton bypass means that nil is never read.
	if len(passA) > 1 {
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
	} else if len(passA) == 1 {
		// Singleton path: skip the probe; lone cred enters with
		// lastUsage=nil. Capture loop and Promote work fine on this
		// shape; the scheduler bypass guarantees the nil is never
		// dereferenced via computeFeasibility.
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

	// Rejected creds are dropped from the pool but never fatal on
	// their own — the operator's intent is "use these creds for
	// load-balancing", and dropping a bad one is closer to that
	// intent than aborting the whole session over one transient
	// 429 / refresh blip. Only fail if NO cred survives (handled
	// below by the "len(pool.entries) == 0" path).

	// Sort admitted creds by feasibility (highest first; ID lex tie-break).
	sort.Slice(admitted, func(i, j int) bool {
		if admitted[i].feasibility != admitted[j].feasibility {
			return admitted[i].feasibility > admitted[j].feasibility
		}
		return admitted[i].cred.ID < admitted[j].cred.ID
	})

	// Capture loop: try each admitted cred in feasibility order.
	// Track which IDs have already failed capture so they're not
	// admitted as candidates after a later cred succeeds.
	var captureRejects []string
	captureFailedIDs := make(map[string]bool)
	for _, ad := range admitted {
		var headers http.Header
		if !skipCapture {
			h, cerr := captureCredFn(ad.cred, prompt) // empty → DefaultCapturePrompt in RunCapture
			if cerr != nil {
				msg := fmt.Sprintf("%s(%s): capture failed: %v", credLogName(ad.cred), shortID(ad.cred.ID), cerr)
				captureRejects = append(captureRejects, msg)
				captureFailedIDs[ad.cred.ID] = true
				fmt.Fprintf(errLog(), "ccm: skipping %s\n", msg)
				// Capture failure on an explicitly-named cred used to
				// be fatal; relaxed to "drop and continue" so a
				// flaky claude install on one cred doesn't abort the
				// whole load-balance session. Operator sees the
				// per-cred skip log; only zero survivors is fatal.
				continue
			}
			headers = h
		}
		// skipCapture=true → headers stays nil. Promote stores nil
		// (guarded by `if headers != nil`); LocalProxy.director never
		// reads activatedHeaders in launch mode.
		ad.entry.status = statusActivated
		ad.entry.captured = headers
		pool.entries[ad.cred.ID] = ad.entry
		pool.activated = ad.cred.ID

		// Add the rest of the admitted creds as candidates, EXCLUDING
		// any whose capture failed earlier in this loop. Remaining
		// creds get captured on their first promotion attempt by the
		// scheduler.
		for _, other := range admitted {
			if other.cred.ID == ad.cred.ID {
				continue
			}
			if captureFailedIDs[other.cred.ID] {
				continue
			}
			pool.entries[other.cred.ID] = other.entry
		}
		pool.singleton = len(pool.entries) == 1

		fmt.Fprintf(errLog(), "ccm: load-balance pool: %d candidates, initial activated %s(%s) (lifetime %s)\n",
			len(pool.entries), credLogName(ad.cred), shortID(ad.cred.ID), formatLifetime(ad.feasibility))
		return pool, ad.cred, nil
	}

	// All captures failed (implicit-mode case).
	if len(captureRejects) > 0 {
		return nil, nil, fmt.Errorf("ccm: no candidate could be captured:\n  %s",
			joinLines(captureRejects))
	}
	if len(rejections) > 0 {
		return nil, nil, fmt.Errorf("ccm: no usable credentials in pool:\n  %s",
			joinLines(rejections))
	}
	return nil, nil, errors.New("ccm: no credentials in store; run `ccm login` first")
}

// resolvePoolArgs turns CLI arguments into a deduped list of
// *store.Credential. Empty args = every credential in the store.
func resolvePoolArgs(args []string) ([]*store.Credential, error) {
	if len(args) == 0 {
		return store.List()
	}
	seen := make(map[string]struct{})
	var out []*store.Credential
	for _, a := range args {
		c, err := store.Resolve(a)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", a, err)
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
