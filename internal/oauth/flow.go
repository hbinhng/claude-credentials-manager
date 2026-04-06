package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// Login runs the OAuth Authorization Code + PKCE flow using the copy-code
// callback. The user is directed to Claude's authorize page which displays
// a code after authentication. The user pastes that code back into the CLI.
func Login(readCode func() (string, error)) (*TokenResponse, error) {
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE: %w", err)
	}

	authURL := buildAuthorizeURL(pkce)

	fmt.Println("\nOpen this URL in your browser to authenticate:")
	fmt.Printf("\n  %s\n\n", authURL)
	tryOpenBrowser(authURL)

	fmt.Print("Paste the code here: ")
	code, err := readCode()
	if err != nil {
		return nil, fmt.Errorf("read code: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("no code provided")
	}

	fmt.Println("Exchanging code for tokens...")
	return exchangeCode(code, pkce.State, pkce)
}

func buildAuthorizeURL(pkce *PKCEParams) string {
	params := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {strings.Join(Scopes, " ")},
		"code_challenge":        {pkce.CodeChallenge},
		"code_challenge_method": {CodeChallengeMethod},
		"state":                 {pkce.State},
	}
	return AuthorizeURL + "?" + params.Encode()
}

func exchangeCode(code, state string, pkce *PKCEParams) (*TokenResponse, error) {
	// The auth code may contain #state appended
	authCode := code
	codeState := ""
	if idx := strings.Index(authCode, "#"); idx >= 0 {
		codeState = authCode[idx+1:]
		authCode = authCode[:idx]
	}
	if codeState == "" {
		codeState = state
	}

	payload := map[string]string{
		"code":          authCode,
		"state":         codeState,
		"grant_type":    "authorization_code",
		"client_id":     ClientID,
		"redirect_uri":  RedirectURI,
		"code_verifier": pkce.CodeVerifier,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(TokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(respBody, &tokens); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	return &tokens, nil
}

func tryOpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return
	}
	cmd.Start()
}
