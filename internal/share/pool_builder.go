package share

import (
	"errors"
	"fmt"
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
func BuildPool(args []string) (*credPool, *store.Credential, error) {
	explicit := len(args) > 0
	resolved, err := resolvePoolArgs(args)
	if err != nil {
		return nil, nil, err
	}

	pool := &credPool{entries: make(map[string]*poolEntry)}
	var rejections []string
	var initialID string
	bestFeasibility := -1.0

	for _, cred := range resolved {
		state := newCredState(cred)
		if _, ferr := state.Fresh(); ferr != nil {
			msg := fmt.Sprintf("%s(%s): refresh failed: %v", credLogName(cred), shortID(cred.ID), ferr)
			rejections = append(rejections, msg)
			fmt.Fprintf(errLog(), "ccm share: skipping %s\n", msg)
			continue
		}
		info := oauth.FetchUsageFn(cred.ClaudeAiOauth.AccessToken)
		if info == nil || info.Error != "" {
			reason := "usage probe returned nil"
			if info != nil {
				reason = info.Error
			}
			msg := fmt.Sprintf("%s(%s): %s", credLogName(cred), shortID(cred.ID), reason)
			rejections = append(rejections, msg)
			fmt.Fprintf(errLog(), "ccm share: skipping %s\n", msg)
			continue
		}
		f := computeFeasibility(info, timeNow())
		entry := &poolEntry{
			state:           state,
			status:          statusCandidate,
			lastUsage:       info,
			lastUsageAt:     timeNow(),
			lastFeasibility: f,
		}
		pool.entries[cred.ID] = entry
		if f > bestFeasibility || (f == bestFeasibility && cred.ID < initialID) {
			bestFeasibility = f
			initialID = cred.ID
		}
	}

	if explicit && len(rejections) > 0 {
		return nil, nil, fmt.Errorf("ccm share: explicitly named credential(s) rejected:\n  %s",
			joinLines(rejections))
	}
	if len(pool.entries) == 0 {
		if len(rejections) > 0 {
			return nil, nil, fmt.Errorf("ccm share: no usable credentials in pool:\n  %s",
				joinLines(rejections))
		}
		return nil, nil, errors.New("ccm share: no credentials in store; run `ccm login` first")
	}

	pool.entries[initialID].status = statusActivated
	pool.activated = initialID
	pool.singleton = len(pool.entries) == 1

	initialCred := pool.entries[initialID].state.credPtr()
	fmt.Fprintf(errLog(), "ccm share: load-balance pool: %d candidates, initial activated %s(%s) (feasibility %.3f)\n",
		len(pool.entries), credLogName(initialCred), shortID(initialID), bestFeasibility)

	return pool, initialCred, nil
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
