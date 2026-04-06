package oauth

const (
	ClientID            = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	AuthorizeURL        = "https://claude.ai/oauth/authorize"
	RedirectURI         = "https://platform.claude.com/oauth/code/callback"
	CodeChallengeMethod = "S256"
)

// TokenURL is a var (not const) so tests can override it with httptest servers.
var TokenURL = "https://console.anthropic.com/v1/oauth/token"

var Scopes = []string{
	"user:inference",
	"user:profile",
	"user:sessions:claude_code",
	"user:mcp_servers",
}
