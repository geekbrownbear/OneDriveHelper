// Package ninja talks to the NinjaOne agent's command-line tool
// (ninjarmm-cli.exe) to read and write custom fields from the service.
//
// The tool is installed with the NinjaOne agent and must run as SYSTEM, which
// the OneDrive Helper service is. Values are written with "set <field> <value>"
// or, for long values such as the WYSIWYG table, "set --stdin <field>" with
// the value piped in. Fields are read with "get <field>".
package ninja

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotInstalled means the NinjaOne agent (and so its CLI) is not on this PC.
var ErrNotInstalled = errors.New("ninjarmm-cli.exe not found; is the NinjaOne agent installed?")

// Timeout bounds every CLI call so a wedged agent never stalls the service.
const Timeout = 60 * time.Second

// MaxValueBytes is the most we send to one field. NinjaOne collapses fields
// over 10,000 characters in the UI, and the WYSIWYG renderer in
// internal/status keeps its output well under this.
const MaxValueBytes = 200 << 10

// CLI runs ninjarmm-cli.
type CLI struct {
	Path string // full path of ninjarmm-cli.exe
}

// DefaultPath is where the NinjaOne agent installs its CLI on Windows.
func DefaultPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "NinjaRMMAgent", "ninjarmm-cli.exe")
}

// Find returns the CLI if the NinjaOne agent is installed. The path is fixed
// (not taken from the registry or PATH) and must be a regular file, so a
// user-planted executable is never run as SYSTEM.
func Find() (*CLI, error) {
	return FindAt(DefaultPath())
}

// FindAt is Find for a specific path (tests).
func FindAt(path string) (*CLI, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrNotInstalled
	}
	return &CLI{Path: path}, nil
}

// Set writes a short value to a custom field.
func (c *CLI) Set(ctx context.Context, field, value string) error {
	if err := checkField(field); err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("value for %s is %d bytes (limit %d)", field, len(value), MaxValueBytes)
	}
	// Long or multi-line values go through stdin; the command line would mangle them.
	if len(value) > 2000 || strings.ContainsAny(value, "\r\n\"") {
		return c.SetStdin(ctx, field, value)
	}
	_, err := c.run(ctx, nil, "set", field, value)
	return err
}

// SetStdin writes a value through the CLI's standard input (for HTML and
// multi-line text).
func (c *CLI) SetStdin(ctx context.Context, field, value string) error {
	if err := checkField(field); err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("value for %s is %d bytes (limit %d)", field, len(value), MaxValueBytes)
	}
	_, err := c.run(ctx, strings.NewReader(value), "set", "--stdin", field)
	return err
}

// Get reads a custom field. An unset field returns "" and no error.
func (c *CLI) Get(ctx context.Context, field string) (string, error) {
	if err := checkField(field); err != nil {
		return "", err
	}
	out, err := c.run(ctx, nil, "get", field)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\r\n"), nil
}

func checkField(field string) error {
	if field == "" || strings.ContainsAny(field, " \t\r\n\"'") {
		return fmt.Errorf("%q is not a valid custom field name", field)
	}
	return nil
}

func (c *CLI) run(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, args...)
	cmd.Dir = filepath.Dir(c.Path)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	hideWindow(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("ninjarmm-cli %s: timed out after %s", args[0], Timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg != "" {
			return "", fmt.Errorf("ninjarmm-cli %s %s: %w: %s", args[0], lastArg(args), err, firstLine(msg))
		}
		return "", fmt.Errorf("ninjarmm-cli %s %s: %w", args[0], lastArg(args), err)
	}
	return out.String(), nil
}

func lastArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
