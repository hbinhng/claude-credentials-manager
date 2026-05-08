package codexoauth_test

import (
	"errors"
	"testing"

	codexoauth "github.com/hbinhng/claude-credentials-manager/internal/codex/oauth"
)

func TestErrorSentinels_AreDistinctNonNil(t *testing.T) {
	all := []error{
		codexoauth.ErrPortInUse,
		codexoauth.ErrCallbackTimeout,
		codexoauth.ErrStateMismatch,
		codexoauth.ErrAuthDenied,
		codexoauth.ErrRefreshRotated,
		codexoauth.ErrTokenEndpoint,
	}
	for i := 0; i < len(all); i++ {
		if all[i] == nil {
			t.Fatalf("err[%d] is nil", i)
		}
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Fatalf("err[%d] aliases err[%d]", i, j)
			}
		}
	}
}
