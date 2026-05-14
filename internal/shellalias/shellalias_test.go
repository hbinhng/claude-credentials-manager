package shellalias

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeShell is a Shell that points its AliasFile/RcFiles at temp paths
// for testing. EmitAlias uses the POSIX emitter so we get realistic
// output.
type fakeShell struct {
	name      string
	aliasPath string
	rcPath    string
}

func (f *fakeShell) Name() string                  { return f.name }
func (f *fakeShell) AliasFile() string             { return f.aliasPath }
func (f *fakeShell) RcFiles() ([]string, error)    { return []string{f.rcPath}, nil }
func (f *fakeShell) Quote(s string) string         { return posixQuote(s) }
func (f *fakeShell) EmitAlias(n string, p []string) string {
	return (&posixShell{name: f.name}).EmitAlias(n, p)
}

func newFakeShell(t *testing.T, name string) *fakeShell {
	t.Helper()
	d := t.TempDir()
	return &fakeShell{
		name:      name,
		aliasPath: filepath.Join(d, "aliases.sh"),
		rcPath:    filepath.Join(d, "rc"),
	}
}

func TestInstall_WritesAliasFileAndRc(t *testing.T) {
	fs := newFakeShell(t, "bash")
	errs := Install("cld", []string{"--load-balance", "c"}, []Shell{fs}, false)
	if errs[0] != nil {
		t.Fatalf("install err: %v", errs[0])
	}
	got, err := os.ReadFile(fs.aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "# ccm-alias:begin:cld") ||
		!strings.Contains(string(got), `cld() { ccm launch '--load-balance' 'c' "$@"; }`) {
		t.Fatalf("alias file: %s", got)
	}
	rc, err := os.ReadFile(fs.rcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rc), "# ccm-aliases:begin") ||
		!strings.Contains(string(rc), fs.aliasPath) {
		t.Fatalf("rc: %s", rc)
	}
}

func TestInstall_RewritesAliasIdempotent(t *testing.T) {
	fs := newFakeShell(t, "bash")
	Install("cld", []string{"a"}, []Shell{fs}, false)
	errs := Install("cld", []string{"b"}, []Shell{fs}, false)
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	got, _ := os.ReadFile(fs.aliasPath)
	if strings.Contains(string(got), "'a'") {
		t.Fatalf("did not replace: %s", got)
	}
	if !strings.Contains(string(got), "'b'") {
		t.Fatalf("missing new payload: %s", got)
	}
	// Only one begin sentinel for cld.
	if strings.Count(string(got), "# ccm-alias:begin:cld") != 1 {
		t.Fatalf("expected one block, got: %s", got)
	}
}

func TestInstall_RcStaysIdempotent(t *testing.T) {
	fs := newFakeShell(t, "bash")
	Install("a", []string{"x"}, []Shell{fs}, false)
	Install("b", []string{"y"}, []Shell{fs}, false)
	rc, _ := os.ReadFile(fs.rcPath)
	if strings.Count(string(rc), "# ccm-aliases:begin") != 1 {
		t.Fatalf("rc not idempotent: %s", rc)
	}
}

func TestInstall_CreatesParentDirs(t *testing.T) {
	d := t.TempDir()
	fs := &fakeShell{
		name:      "fish",
		aliasPath: filepath.Join(d, "ccm", "aliases.fish"),
		rcPath:    filepath.Join(d, "config", "fish", "config.fish"),
	}
	errs := Install("cld", []string{"x"}, []Shell{fs}, false)
	if errs[0] != nil {
		t.Fatalf("err: %v", errs[0])
	}
}

func TestList_Empty(t *testing.T) {
	t.Setenv("CCM_HOME", t.TempDir())
	got, err := List()
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestList_AcrossShells(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "aliases.sh"),
		[]byte("# ccm-alias:begin:cld\nx\n# ccm-alias:end:cld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "aliases.fish"),
		[]byte("# ccm-alias:begin:cld-fish\ny\n# ccm-alias:end:cld-fish\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	// aliases.sh is shared by bash+zsh, so the first entry should have both.
	if !reflect.DeepEqual(got[0].Shells, []string{"bash", "zsh"}) {
		t.Fatalf("first entry shells: got %v want [bash zsh]", got[0].Shells)
	}
	// aliases.fish is fish only.
	if !reflect.DeepEqual(got[1].Shells, []string{"fish"}) {
		t.Fatalf("second entry shells: got %v want [fish]", got[1].Shells)
	}
}

func TestRemove_NotFound(t *testing.T) {
	t.Setenv("CCM_HOME", t.TempDir())
	if err := Remove("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestRemove_AcrossShells(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	for _, fname := range []string{"aliases.sh", "aliases.fish", "aliases.ps1"} {
		if err := os.WriteFile(filepath.Join(home, fname),
			[]byte("# ccm-alias:begin:cld\nx\n# ccm-alias:end:cld\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove("cld"); err != nil {
		t.Fatal(err)
	}
	for _, fname := range []string{"aliases.sh", "aliases.fish", "aliases.ps1"} {
		got, _ := os.ReadFile(filepath.Join(home, fname))
		if strings.Contains(string(got), "ccm-alias:begin:cld") {
			t.Fatalf("%s still has block: %s", fname, got)
		}
	}
}

func TestInstall_AliasFileWriteError(t *testing.T) {
	// AliasFile pointing inside an unwritable file masquerading as a dir.
	d := t.TempDir()
	conflict := filepath.Join(d, "conflict")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := &fakeShell{
		name:      "bash",
		aliasPath: filepath.Join(conflict, "aliases.sh"), // dir = file → MkdirAll fails
		rcPath:    filepath.Join(d, "rc"),
	}
	errs := Install("cld", []string{"x"}, []Shell{fs}, false)
	if errs[0] == nil {
		t.Fatal("expected MkdirAll error")
	}
}

func TestInstall_RcResolverError(t *testing.T) {
	// Shell whose RcFiles() returns an error must propagate.
	d := t.TempDir()
	fs := &errRcShell{aliasPath: filepath.Join(d, "aliases.sh")}
	errs := Install("cld", []string{"x"}, []Shell{fs}, false)
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "resolve") {
		t.Fatalf("got %v", errs[0])
	}
}

type errRcShell struct {
	aliasPath string
}

func (e *errRcShell) Name() string                  { return "bash" }
func (e *errRcShell) AliasFile() string             { return e.aliasPath }
func (e *errRcShell) RcFiles() ([]string, error)    { return nil, errors.New("no rc") }
func (e *errRcShell) Quote(s string) string         { return posixQuote(s) }
func (e *errRcShell) EmitAlias(n string, p []string) string {
	return (&posixShell{name: "bash"}).EmitAlias(n, p)
}

func TestList_DedupesIdenticalBlocks(t *testing.T) {
	// aliases.sh is read once (not separately for bash and zsh), so an
	// alias there produces exactly one entry with Shells = ["bash", "zsh"].
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "aliases.sh"),
		[]byte("# ccm-alias:begin:cld\nfoo\n# ccm-alias:end:cld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "cld" {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got[0].Shells, []string{"bash", "zsh"}) {
		t.Fatalf("shells: got %v want [bash zsh]", got[0].Shells)
	}
}

// --- additional coverage-extension tests ---

// aliasUnreadableShell has an alias file that exists but is unreadable
// (passes MkdirAll, fails ReadFile).
type aliasUnreadableShell struct {
	aliasPath string
	rcPath    string
}

func (s *aliasUnreadableShell) Name() string                  { return "bash" }
func (s *aliasUnreadableShell) AliasFile() string             { return s.aliasPath }
func (s *aliasUnreadableShell) RcFiles() ([]string, error)    { return []string{s.rcPath}, nil }
func (s *aliasUnreadableShell) Quote(arg string) string       { return posixQuote(arg) }
func (s *aliasUnreadableShell) EmitAlias(n string, p []string) string {
	return (&posixShell{name: "bash"}).EmitAlias(n, p)
}

func TestInstall_AliasReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	d := t.TempDir()
	aliasPath := filepath.Join(d, "aliases.sh")
	// Create file then make it unreadable.
	if err := os.WriteFile(aliasPath, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	sh := &aliasUnreadableShell{aliasPath: aliasPath, rcPath: filepath.Join(d, "rc")}
	errs := Install("cld", []string{"x"}, []Shell{sh}, false)
	if errs[0] == nil {
		t.Fatal("expected read error for alias file")
	}
}

func TestInstall_AliasWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	d := t.TempDir()
	aliasPath := filepath.Join(d, "aliases.sh")
	// Create readable but not writable.
	if err := os.WriteFile(aliasPath, []byte(""), 0o444); err != nil {
		t.Fatal(err)
	}
	sh := &aliasUnreadableShell{aliasPath: aliasPath, rcPath: filepath.Join(d, "rc")}
	errs := Install("cld", []string{"x"}, []Shell{sh}, false)
	if errs[0] == nil {
		t.Fatal("expected write error for alias file")
	}
}

func TestInstall_RcDirCreateError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	d := t.TempDir()
	// rc parent is a regular file — MkdirAll will fail.
	conflict := filepath.Join(d, "conflict")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sh := &fakeShell{
		name:      "bash",
		aliasPath: filepath.Join(d, "aliases.sh"),
		rcPath:    filepath.Join(conflict, "rc"), // parent is a file
	}
	errs := Install("cld", []string{"x"}, []Shell{sh}, false)
	if errs[0] == nil || !strings.Contains(errs[0].Error(), "create rc dir") {
		t.Fatalf("expected rc dir creation error, got: %v", errs[0])
	}
}

func TestInstall_RcReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	d := t.TempDir()
	rcPath := filepath.Join(d, "rc")
	if err := os.WriteFile(rcPath, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	sh := &fakeShell{
		name:      "bash",
		aliasPath: filepath.Join(d, "aliases.sh"),
		rcPath:    rcPath,
	}
	errs := Install("cld", []string{"x"}, []Shell{sh}, false)
	if errs[0] == nil {
		t.Fatal("expected read error for rc file")
	}
}

func TestInstall_RcWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	d := t.TempDir()
	rcPath := filepath.Join(d, "rc")
	// Readable but not writable; no sentinel so snippet will be appended.
	if err := os.WriteFile(rcPath, []byte(""), 0o444); err != nil {
		t.Fatal(err)
	}
	sh := &fakeShell{
		name:      "bash",
		aliasPath: filepath.Join(d, "aliases.sh"),
		rcPath:    rcPath,
	}
	errs := Install("cld", []string{"x"}, []Shell{sh}, false)
	if errs[0] == nil {
		t.Fatal("expected write error for rc file")
	}
}

func TestList_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	// Create aliases.sh as unreadable so List returns an error.
	aliasPath := filepath.Join(home, "aliases.sh")
	if err := os.WriteFile(aliasPath, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := List()
	if err == nil {
		t.Fatal("expected read error from List")
	}
}

func TestRemove_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	aliasPath := filepath.Join(home, "aliases.sh")
	if err := os.WriteFile(aliasPath, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	err := Remove("cld")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected read error, got: %v", err)
	}
}

func TestRemove_WriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	aliasPath := filepath.Join(home, "aliases.sh")
	// Write a block then lock the file.
	if err := os.WriteFile(aliasPath,
		[]byte("# ccm-alias:begin:cld\nx\n# ccm-alias:end:cld\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	err := Remove("cld")
	if err == nil {
		t.Fatal("expected write error from Remove")
	}
}

func TestList_PayloadExtraction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	content := "# ccm-alias:begin:cld\n# ccm-alias:payload:[\"--load-balance\",\"cred-a\"]\ncld() { ccm launch '--load-balance' 'cred-a' \"$@\"; }\n# ccm-alias:end:cld\n"
	if err := os.WriteFile(filepath.Join(home, "aliases.sh"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	got := entries[0]
	wantShells := []string{"bash", "zsh"}
	if !reflect.DeepEqual(got.Shells, wantShells) {
		t.Fatalf("shells: got %v want %v", got.Shells, wantShells)
	}
	wantPayload := []string{"--load-balance", "cred-a"}
	if !reflect.DeepEqual(got.Payload, wantPayload) {
		t.Fatalf("payload: got %v want %v", got.Payload, wantPayload)
	}
}

func TestList_MissingPayloadCommentReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	// Block without the payload comment.
	content := "# ccm-alias:begin:cld\ncld() { ccm launch x; }\n# ccm-alias:end:cld\n"
	if err := os.WriteFile(filepath.Join(home, "aliases.sh"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Payload != nil {
		t.Fatalf("expected nil payload, got %+v", entries)
	}
}

func TestList_CorruptedPayloadReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCM_HOME", home)
	content := "# ccm-alias:begin:cld\n# ccm-alias:payload:not-valid-json\ncld() { ccm launch x; }\n# ccm-alias:end:cld\n"
	if err := os.WriteFile(filepath.Join(home, "aliases.sh"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Payload != nil {
		t.Fatalf("expected nil payload, got %v", entries[0].Payload)
	}
}

func TestInstall_MultipleRcPathsAllGetSnippet(t *testing.T) {
	d := t.TempDir()
	rcA := filepath.Join(d, "profile-a.ps1")
	rcB := filepath.Join(d, "profile-b.ps1")
	fs := &multiRcShell{
		name:      "pwsh",
		aliasPath: filepath.Join(d, "aliases.ps1"),
		rcPaths:   []string{rcA, rcB},
	}
	errs := Install("cld", []string{"x"}, []Shell{fs}, false)
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	for _, p := range []string{rcA, rcB} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("rc %s: %v", p, err)
		}
		if !strings.Contains(string(got), "# ccm-aliases:begin") {
			t.Fatalf("rc %s missing sentinel: %s", p, got)
		}
	}
}

type multiRcShell struct {
	name      string
	aliasPath string
	rcPaths   []string
}

func (m *multiRcShell) Name() string               { return m.name }
func (m *multiRcShell) AliasFile() string          { return m.aliasPath }
func (m *multiRcShell) RcFiles() ([]string, error) { return m.rcPaths, nil }
func (m *multiRcShell) Quote(s string) string      { return pwshQuote(s) }
func (m *multiRcShell) EmitAlias(n string, p []string) string {
	return (&pwshShell{}).EmitAlias(n, p)
}
