package shellalias

import (
	"strings"
	"testing"
)

func TestAliasBlocks_ParseEmpty(t *testing.T) {
	got := parseAliasBlocks(nil)
	if len(got) != 0 {
		t.Fatalf("got %d blocks", len(got))
	}
}

func TestAliasBlocks_ParseOne(t *testing.T) {
	in := []byte("# ccm-alias:begin:cld\ncld() { ccm launch; }\n# ccm-alias:end:cld\n")
	got := parseAliasBlocks(in)
	if len(got) != 1 || got[0].Name != "cld" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].Body, "ccm launch") {
		t.Fatalf("body: %q", got[0].Body)
	}
}

func TestAliasBlocks_ParseMultiple(t *testing.T) {
	in := []byte(
		"# ccm-alias:begin:cld\n" +
			"cld() { ccm launch; }\n" +
			"# ccm-alias:end:cld\n" +
			"# ccm-alias:begin:cld-prod\n" +
			"cld-prod() { ccm launch --via x; }\n" +
			"# ccm-alias:end:cld-prod\n")
	got := parseAliasBlocks(in)
	if len(got) != 2 || got[0].Name != "cld" || got[1].Name != "cld-prod" {
		t.Fatalf("got %+v", got)
	}
}

func TestAliasBlocks_IgnoresUnrelated(t *testing.T) {
	in := []byte("# random comment\necho hi\n# ccm-alias:begin:cld\ncld() {}\n# ccm-alias:end:cld\n# trailing\n")
	got := parseAliasBlocks(in)
	if len(got) != 1 || got[0].Name != "cld" {
		t.Fatalf("got %+v", got)
	}
}

func TestAliasBlocks_UpsertNew(t *testing.T) {
	out := upsertAliasBlock(nil, "cld", "cld() { ccm launch; }")
	want := "# ccm-alias:begin:cld\ncld() { ccm launch; }\n# ccm-alias:end:cld\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestAliasBlocks_UpsertReplaces(t *testing.T) {
	in := []byte("# ccm-alias:begin:cld\nOLD\n# ccm-alias:end:cld\n")
	out := upsertAliasBlock(in, "cld", "NEW")
	want := "# ccm-alias:begin:cld\nNEW\n# ccm-alias:end:cld\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestAliasBlocks_UpsertAppendsToOther(t *testing.T) {
	in := []byte("# ccm-alias:begin:other\nOTHER\n# ccm-alias:end:other\n")
	out := upsertAliasBlock(in, "cld", "NEW")
	want := "# ccm-alias:begin:other\nOTHER\n# ccm-alias:end:other\n" +
		"# ccm-alias:begin:cld\nNEW\n# ccm-alias:end:cld\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestAliasBlocks_RemoveOne(t *testing.T) {
	in := []byte(
		"# ccm-alias:begin:a\nA\n# ccm-alias:end:a\n" +
			"# ccm-alias:begin:b\nB\n# ccm-alias:end:b\n")
	out, ok := removeAliasBlock(in, "a")
	if !ok {
		t.Fatal("expected ok")
	}
	want := "# ccm-alias:begin:b\nB\n# ccm-alias:end:b\n"
	if string(out) != want {
		t.Fatalf("got %q", out)
	}
}

func TestAliasBlocks_RemoveMissing(t *testing.T) {
	in := []byte("# ccm-alias:begin:a\nA\n# ccm-alias:end:a\n")
	_, ok := removeAliasBlock(in, "missing")
	if ok {
		t.Fatal("expected !ok for missing alias")
	}
}

func TestAliasBlocks_ParseUnterminatedBegin(t *testing.T) {
	got := parseAliasBlocks([]byte("# ccm-alias:begin:cld\nbody-no-end\n"))
	if len(got) != 0 {
		t.Fatalf("got %d blocks; expected 0", len(got))
	}
}

func TestAliasBlocks_UpsertOverUnterminatedBegin(t *testing.T) {
	// Existing begin with no end is treated as missing → new block appended.
	in := []byte("# ccm-alias:begin:cld\nstale\n")
	out := upsertAliasBlock(in, "cld", "NEW")
	if !strings.Contains(string(out), "# ccm-alias:end:cld") {
		t.Fatalf("missing end sentinel: %s", out)
	}
}

func TestAliasBlocks_UpsertAppendsToContentMissingTrailingNewline(t *testing.T) {
	in := []byte("# ccm-alias:begin:a\nA\n# ccm-alias:end:a")
	out := upsertAliasBlock(in, "b", "B")
	// must end with the new block's trailing newline and must include "a"'s end
	if !strings.HasSuffix(string(out), "# ccm-alias:end:b\n") {
		t.Fatalf("missing trailing newline: %q", out)
	}
}

func TestAliasBlocks_UpsertReplacesMiddle(t *testing.T) {
	// Block has content both before and after it — exercises the in-place
	// replace path where before != "" and after != "".
	in := []byte(
		"# ccm-alias:begin:a\nA\n# ccm-alias:end:a\n" +
			"# ccm-alias:begin:cld\nOLD\n# ccm-alias:end:cld\n" +
			"# ccm-alias:begin:b\nB\n# ccm-alias:end:b\n")
	out := upsertAliasBlock(in, "cld", "NEW")
	want := "# ccm-alias:begin:a\nA\n# ccm-alias:end:a\n" +
		"# ccm-alias:begin:cld\nNEW\n# ccm-alias:end:cld\n" +
		"# ccm-alias:begin:b\nB\n# ccm-alias:end:b\n"
	if string(out) != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
}

func TestAliasBlocks_RemoveLast(t *testing.T) {
	in := []byte(
		"# ccm-alias:begin:a\nA\n# ccm-alias:end:a\n" +
			"# ccm-alias:begin:b\nB\n# ccm-alias:end:b\n")
	out, ok := removeAliasBlock(in, "b")
	if !ok {
		t.Fatal("expected ok")
	}
	if strings.Contains(string(out), "begin:b") {
		t.Fatalf("b not removed: %s", out)
	}
	if !strings.Contains(string(out), "begin:a") {
		t.Fatalf("a was clobbered: %s", out)
	}
}
