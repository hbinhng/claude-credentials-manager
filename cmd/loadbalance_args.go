package cmd

import "strings"

// splitCommaArgs flattens positional credential args by splitting any
// element on commas. The user can write --load-balance with either
// space-separated args ("a b c"), comma-separated within a single
// shell token ("a,b,c"), or any mix ("a,b c"). Empty fragments
// (from trailing commas or "a,,b") are dropped.
//
// Whitespace inside a comma-separated token is preserved as part of
// the cred name — credential names don't allow whitespace today, so
// a token like "alice ,bob" would yield ["alice ", "bob"] which then
// fails to resolve, surfacing the malformed input as a resolution
// error rather than silently swallowing it.
func splitCommaArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.Contains(a, ",") {
			if a != "" {
				out = append(out, a)
			}
			continue
		}
		for _, p := range strings.Split(a, ",") {
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
