package usage

import "testing"

func TestNormalizeModelID(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"claude-opus-4-7-20251217", "claude-opus-4-7"},
		{"claude-sonnet-4-6-20250101", "claude-sonnet-4-6"},
		{"claude-haiku-4-5-20240920", "claude-haiku-4-5"},
		{"claude-future-9-9-20991231", "claude-future-9-9"},
		{"claude-no-date-suffix", "claude-no-date-suffix"},
		{"", ""},
		{"only-7-digits-1234567", "only-7-digits-1234567"},
		{"too-many-digits-123456789", "too-many-digits-123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizeModelID(tt.in); got != tt.want {
				t.Errorf("NormalizeModelID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestModelDisplay(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"claude-opus-4-7-20251217", "Opus 4.7"},
		{"claude-sonnet-4-6-20250101", "Sonnet 4.6"},
		{"claude-haiku-4-5-20240920", "Haiku 4.5"},
		{"claude-unknown-model-20991231", "claude-unknown-model"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ModelDisplay(tt.in); got != tt.want {
				t.Errorf("ModelDisplay(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
