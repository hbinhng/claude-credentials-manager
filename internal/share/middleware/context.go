// Package middleware contains the common pipeline steps shared between
// the claude and codex flows in the share proxy.
package middleware

// contextKey is unexported to prevent collisions with other packages'
// context values.
type contextKey int

const (
	// keyOriginalModel holds the inbound model string before any
	// AliasRewrite mutation. Set by AliasRewrite; consumed by logging
	// and the codex die-fast detector (post-alias model is on the wire,
	// pre-alias model is in this context value).
	keyOriginalModel contextKey = iota

	// keyAliasMatched is true if AliasRewrite found a matching rule
	// for this request. Codex middleware uses this to decide
	// translate (true) vs pass-through (false).
	keyAliasMatched

	// keyEffectiveModel holds the post-alias-rewrite model string.
	keyEffectiveModel
)

// Public accessors for tests and other packages.

// OriginalModel returns the pre-alias model string from ctx, or "" if
// AliasRewrite did not run.
func OriginalModel(ctx interface{ Value(any) any }) string {
	v, _ := ctx.Value(keyOriginalModel).(string)
	return v
}

// EffectiveModel returns the post-alias model string from ctx, or "" if
// AliasRewrite did not run.
func EffectiveModel(ctx interface{ Value(any) any }) string {
	v, _ := ctx.Value(keyEffectiveModel).(string)
	return v
}

// AliasMatched returns whether the inbound model matched a rule.
func AliasMatched(ctx interface{ Value(any) any }) bool {
	v, _ := ctx.Value(keyAliasMatched).(bool)
	return v
}
