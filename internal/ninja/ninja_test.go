package ninja

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeCLI writes a shell script that records its arguments and stdin to a
// log file and prints a canned value for "get".
func fakeCLI(t *testing.T) (*CLI, string) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"args: $*\" >> \"" + logFile + "\"\n" +
		"if [ \"$2\" = \"--stdin\" ]; then echo \"stdin: $(cat)\" >> \"" + logFile + "\"; fi\n" +
		"if [ \"$1\" = \"get\" ]; then printf 'value of %s\\r\\n' \"$2\"; fi\n" +
		"if [ \"$1\" = \"options\" ]; then printf '11111111-aaaa-4bbb-8ccc-000000000001=Healthy\\r\\n22222222-aaaa-4bbb-8ccc-000000000002=Critical\\r\\n'; fi\n" +
		"if [ \"$2\" = \"boom\" ]; then echo 'no such field' >&2; exit 3; fi\n"
	path := filepath.Join(dir, "ninjarmm-cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := FindAt(path)
	if err != nil {
		t.Fatal(err)
	}
	return c, logFile
}

func TestFindAt(t *testing.T) {
	if _, err := FindAt(filepath.Join(t.TempDir(), "missing.exe")); err != ErrNotInstalled {
		t.Errorf("missing: %v", err)
	}
	if _, err := FindAt(t.TempDir()); err != ErrNotInstalled {
		t.Errorf("directory: %v", err)
	}
}

func TestSetGet(t *testing.T) {
	c, logFile := fakeCLI(t)
	ctx := context.Background()
	if err := c.Set(ctx, "ODHealth", "Healthy"); err != nil {
		t.Fatal(err)
	}
	html := "<table>\n<tr><td>\"x\"</td></tr>\n</table>"
	if err := c.Set(ctx, "ODErrors", html); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "ODBig", strings.Repeat("x", 5000)); err != nil {
		t.Fatal(err)
	}
	v, err := c.Get(ctx, "ODHelperConfig")
	if err != nil || v != "value of ODHelperConfig" {
		t.Errorf("get: %q %v", v, err)
	}
	log, _ := os.ReadFile(logFile)
	for _, want := range []string{"args: set ODHealth Healthy", "args: set --stdin ODErrors", "stdin: <table>", "args: set --stdin ODBig", "args: get ODHelperConfig"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestSetDropdown(t *testing.T) {
	c, logFile := fakeCLI(t)
	ctx := context.Background()
	if err := c.SetDropdown(ctx, "ODHealth", "critical"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(logFile)
	if !strings.Contains(string(log), "args: set ODHealth 22222222-aaaa-4bbb-8ccc-000000000002") {
		t.Errorf("GUID not used:\n%s", log)
	}
	err := c.SetDropdown(ctx, "ODHealth", "Stale")
	if err == nil || !strings.Contains(err.Error(), "options: Critical, Healthy") {
		t.Errorf("missing option: %v", err)
	}
	if got := ParseOptions("  \r\nbad line\r\n=x\r\n"); len(got) != 0 {
		t.Errorf("junk parsed: %v", got)
	}
}

func TestErrors(t *testing.T) {
	c, _ := fakeCLI(t)
	ctx := context.Background()
	err := c.Set(ctx, "boom", "x")
	if err == nil || !strings.Contains(err.Error(), "no such field") {
		t.Errorf("stderr not surfaced: %v", err)
	}
	if err := c.Set(ctx, "bad name", "x"); err == nil {
		t.Error("field name with a space accepted")
	}
	if err := c.SetStdin(ctx, "ODErrors", strings.Repeat("x", MaxValueBytes+1)); err == nil {
		t.Error("oversized value accepted")
	}
}
