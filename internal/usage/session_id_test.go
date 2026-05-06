package usage

import "testing"

func TestIsValidSessionID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"canonical lowercase", "5f2c8c4e-1234-4567-8abc-0123456789ab", true},
		{"canonical uppercase", "5F2C8C4E-1234-4567-8ABC-0123456789AB", true},
		{"mixed case", "5f2c8c4e-1234-4567-8ABC-0123456789ab", true},
		{"empty", "", false},
		{"missing dashes", "5f2c8c4e12344567abc0123456789ab", false},
		{"too short", "5f2c8c4e-1234-4567-8abc-0123456789a", false},
		{"too long", "5f2c8c4e-1234-4567-8abc-0123456789abc", false},
		{"path traversal", "../etc/passwd", false},
		{"slash inside", "5f2c8c4e/1234-4567-8abc-0123456789ab", false},
		{"backslash inside", "5f2c8c4e\\1234-4567-8abc-0123456789ab", false},
		{"null byte", "5f2c8c4e-1234-4567-8abc-0123456789a\x00", false},
		{"non-hex char", "5f2c8c4e-1234-4567-8abc-0123456789xz", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidSessionID(tt.in); got != tt.want {
				t.Errorf("IsValidSessionID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
