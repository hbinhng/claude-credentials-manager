package shellalias

import "testing"

func TestFlavorOf(t *testing.T) {
	cases := map[string]string{
		"bash": "posix",
		"zsh":  "posix",
		"fish": "fish",
		"pwsh": "pwsh",
	}
	registry := map[string]Shell{
		"bash": newBash(), "zsh": newZsh(), "fish": newFish(), "pwsh": newPwsh(),
	}
	for name, want := range cases {
		if got := flavorOf(registry[name]); got != want {
			t.Fatalf("%s: got %q want %q", name, got, want)
		}
	}
}
