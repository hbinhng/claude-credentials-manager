package share

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// passthroughEntryState satisfies poolEntryState for pool entries
// that route through another ccm share via its public tunnel/LAN
// endpoint. The ticket bearer is used as the static OAuth substitute;
// no refresh ever runs for these entries.
type passthroughEntryState struct {
	ticket Ticket
	synth  string // "pt:" + sha256(lowercased-host)[:8]
}

func newPassthroughEntryState(t Ticket) *passthroughEntryState {
	sum := sha256.Sum256([]byte(strings.ToLower(t.Host)))
	id := "pt:" + hex.EncodeToString(sum[:])[:8]
	return &passthroughEntryState{ticket: t, synth: id}
}

func (p *passthroughEntryState) Fresh() (string, error)     { return p.ticket.Token, nil }
func (p *passthroughEntryState) credID() string             { return p.synth }
func (p *passthroughEntryState) credName() string           { return "pt:" + p.ticket.Host }
func (p *passthroughEntryState) credExpiresAt() time.Time   { return time.Time{} }
func (p *passthroughEntryState) credPtr() *store.Credential { return nil }
func (p *passthroughEntryState) upstreamURL() string        { return p.ticket.Scheme + "://" + p.ticket.Host }
func (p *passthroughEntryState) isPassthrough() bool        { return true }

// Compile-time check.
var _ poolEntryState = (*passthroughEntryState)(nil)
