package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

type PKCEParams struct {
	CodeVerifier  string
	CodeChallenge string
	State         string
}

func GeneratePKCE() (*PKCEParams, error) {
	verifier, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	state, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}
	return &PKCEParams{
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
		State:         state,
	}, nil
}

func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
