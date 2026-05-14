package cmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAliasArgs_Create_Minimal(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--load-balance", "cred-a"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := aliasArgs{
		mode:    aliasModeCreate,
		name:    "cld",
		payload: []string{"--load-balance", "cred-a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseAliasArgs_Create_WithDoubleDash(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--load-balance", "cred-a", "--", "-p", "hi"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := aliasArgs{
		mode:    aliasModeCreate,
		name:    "cld",
		payload: []string{"--load-balance", "cred-a", "--", "-p", "hi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseAliasArgs_Create_ShellsAnywhere(t *testing.T) {
	cases := [][]string{
		{"--as", "cld", "--shells", "bash,zsh", "--load-balance", "c"},
		{"--as", "cld", "--load-balance", "c", "--shells", "bash,zsh"},
		{"--shells", "bash,zsh", "--as", "cld", "--load-balance", "c"},
	}
	for i, argv := range cases {
		got, err := parseAliasArgs(argv)
		if err != nil {
			t.Fatalf("case %d err: %v", i, err)
		}
		if got.name != "cld" || !reflect.DeepEqual(got.shells, []string{"bash", "zsh"}) ||
			!reflect.DeepEqual(got.payload, []string{"--load-balance", "c"}) {
			t.Fatalf("case %d got %+v", i, got)
		}
	}
}

func TestParseAliasArgs_Create_Force(t *testing.T) {
	got, err := parseAliasArgs([]string{"--as", "cld", "--force", "--load-balance", "c"})
	if err != nil || !got.force {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_List(t *testing.T) {
	got, err := parseAliasArgs([]string{"--list"})
	if err != nil || got.mode != aliasModeList {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_Remove(t *testing.T) {
	got, err := parseAliasArgs([]string{"--remove", "cld"})
	if err != nil || got.mode != aliasModeRemove || got.name != "cld" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParseAliasArgs_Errors(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no args", nil, "--as <name> is required"},
		{"--as without name", []string{"--as"}, "--as requires a value"},
		{"--shells without value", []string{"--as", "cld", "--shells", "--load-balance"}, "--shells requires"},
		{"--list with extras", []string{"--list", "--as", "cld"}, "--list takes no other arguments"},
		{"--remove without name", []string{"--remove"}, "--remove requires a name"},
		{"--remove with extras", []string{"--remove", "cld", "--force"}, "--remove takes only a name"},
		{"create without payload", []string{"--as", "cld"}, "nothing to capture"},
		{"create with bad name", []string{"--as", "1bad", "--load-balance", "c"}, "invalid alias name"},
		{"create with empty name", []string{"--as", "", "--load-balance", "c"}, "invalid alias name"},
		{"unknown shell", []string{"--as", "cld", "--shells", "csh", "--load-balance", "c"}, "unknown shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAliasArgs(tc.argv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err %v want substring %q", err, tc.want)
			}
		})
	}
}
