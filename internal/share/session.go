package share

import (
	"time"

	"github.com/hbinhng/claude-credentials-manager/internal/store"
)

// Options controls how StartSession brings up a share session.
//
// BindHost:
//   - empty string selects tunnel mode: the proxy binds to a random
//     loopback port and a Cloudflare Quick Tunnel is started in front
//     of it.
//   - non-empty selects LAN-bind mode: the proxy listener wildcards
//     (0.0.0.0) so any host on the local network can reach it, and
//     BindHost is placed in the ticket as the address the remote side
//     should dial. No tunnel is started.
//
// BindPort:
//   - 0 lets the OS pick a port.
//   - non-zero pins the listener to that port.
//
// Debug mirrors CCM_SHARE_DEBUG=1 and enables the per-request director
// logging that already lives in proxy.go.
type Options struct {
	BindHost string
	BindPort int
	Debug    bool
}

// SessionStarter abstracts StartSession for tests and for consumers
// that want to inject a fake (notably internal/serve.Manager).
type SessionStarter interface {
	StartSession(cred *store.Credential, opts Options) (Session, error)
}

// Session is the handle returned by StartSession. It is the ONLY
// surface through which ccm share CLI and ccm serve's manager
// interact with a running share session — proxy, tunnel, capture,
// and ticket plumbing are all internal to the implementation.
//
// Session is safe for concurrent use.
type Session interface {
	Ticket() string             // base64-encoded ticket envelope
	CredID() string
	Mode() string               // "tunnel" | "lan"
	StartedAt() time.Time
	Reach() string              // tunnel URL or "http://<bind-host>:<port>"
	Stop() error                // idempotent
	Done() <-chan struct{}      // closed once Stop finishes
	Err() error                 // non-nil if the session failed after Start
}

// defaultStarter is the production SessionStarter. StartSession is the
// package-level convenience that delegates to it.
type defaultStarter struct{}

// DefaultStarter is what ccm share CLI and ccm serve wire up in
// production. Tests construct their own SessionStarter.
var DefaultStarter SessionStarter = &defaultStarter{}

// StartSession is a convenience wrapper over DefaultStarter.StartSession.
func StartSession(cred *store.Credential, opts Options) (Session, error) {
	return DefaultStarter.StartSession(cred, opts)
}

// StartSession will be implemented in the next task — for now, return
// an explicit not-implemented error so the build stays green and
// compile-time consumers can start wiring against the type.
func (*defaultStarter) StartSession(cred *store.Credential, opts Options) (Session, error) {
	return nil, errNotImplementedYet
}

// errNotImplementedYet is a sentinel that only exists during the
// scaffolding commit. Task 3 removes this symbol entirely as part of
// the real implementation.
var errNotImplementedYet = sessionErr("share.StartSession: not implemented yet")

// sessionErr is the private error type used by the session package.
type sessionErr string

func (e sessionErr) Error() string { return string(e) }
