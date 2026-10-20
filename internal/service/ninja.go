package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/geekbrownbear/OneDriveHelper/internal/config"
	"github.com/geekbrownbear/OneDriveHelper/internal/ninja"
	"github.com/geekbrownbear/OneDriveHelper/internal/status"
)

// PushResult says what PushReport sent.
type PushResult struct {
	Fields map[string]string // field name -> value (HTML abbreviated)
}

// PushReport writes the report to the configured NinjaOne custom fields.
// Fields left blank in the settings are skipped. Every field is attempted;
// the errors are joined.
func PushReport(ctx context.Context, cli *ninja.CLI, cfg config.Config, r status.Report) (PushResult, error) {
	res := PushResult{Fields: map[string]string{}}
	fields := cfg.NinjaFields()
	var errs []error
	const (
		short = iota
		long
		dropdown
	)
	set := func(purpose, value string, kind int) {
		name, ok := fields[purpose]
		if !ok {
			return
		}
		var err error
		switch kind {
		case long:
			err = cli.SetStdin(ctx, name, value)
		case dropdown:
			err = cli.SetDropdown(ctx, name, value)
		default:
			err = cli.Set(ctx, name, value)
		}
		if err != nil {
			errs = append(errs, err)
			return
		}
		shown := value
		if len(shown) > 80 {
			shown = shown[:80] + fmt.Sprintf("... (%d bytes)", len(value))
		}
		res.Fields[name] = shown
	}
	set("health", string(r.Health), dropdown)
	set("staleCount", strconv.Itoa(r.StaleFolderCount), short)
	set("status", status.RenderHTML(r, time.Local), long)
	// Date/time fields take Unix seconds.
	set("lastReport", strconv.FormatInt(r.GeneratedAt.Unix(), 10), short)
	return res, errors.Join(errs...)
}

// PullResult says what PullConfig did.
type PullResult struct {
	Field   string
	Found   bool     // the field had content
	Changed bool     // it differed from what was last applied
	Applied []string // setting names applied
	Hash    string
}

// PullConfig reads the settings field and applies it when it changed since
// lastHash. An empty field is ignored (local settings stay). Invalid content
// is reported as an error and nothing is applied.
func PullConfig(ctx context.Context, cli *ninja.CLI, cfg config.Config, lastHash string) (PullResult, error) {
	name := cfg.NinjaFields()["config"]
	res := PullResult{Field: name}
	if name == "" {
		return res, nil
	}
	raw, err := cli.Get(ctx, name)
	if err != nil {
		return res, err
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return res, nil
	}
	res.Found = true
	sum := sha256.Sum256([]byte(text))
	res.Hash = hex.EncodeToString(sum[:8])
	if res.Hash == lastHash {
		return res, nil
	}
	res.Changed = true
	present, err := config.ImportBytes([]byte(text))
	if err != nil {
		return res, fmt.Errorf("settings in NinjaOne field %s not applied: %w", name, err)
	}
	res.Applied = present
	return res, nil
}

// --- service side ---

// pushRetry is the pause after a failed push before trying again.
const pushRetry = 5 * time.Minute

func (h *handler) ninjaCLI(cfg config.Config) *ninja.CLI {
	if !cfg.NinjaEnabled {
		return nil
	}
	cli, err := ninja.Find()
	if err != nil {
		if !h.ninjaMissingLogged {
			h.log.Printf("NinjaOne: %v (reporting to NinjaOne is off until it appears)", err)
			h.ninjaMissingLogged = true
		}
		return nil
	}
	if h.ninjaMissingLogged {
		h.log.Printf("NinjaOne: found %s", cli.Path)
		h.ninjaMissingLogged = false
	}
	return cli
}

// pullNinjaConfig applies settings from NinjaOne at most once per push interval.
func (h *handler) pullNinjaConfig(cfg config.Config, now time.Time) {
	cli := h.ninjaCLI(cfg)
	if cli == nil || cfg.NinjaFields()["config"] == "" {
		return
	}
	if !h.lastPull.IsZero() && now.Sub(h.lastPull) < cfg.NinjaPush() {
		return
	}
	h.lastPull = now
	res, err := PullConfig(context.Background(), cli, cfg, h.configHash)
	h.notes = nil
	if err != nil {
		h.log.Printf("NinjaOne: %v", err)
		if res.Found {
			h.notes = []string{err.Error()}
			h.configHash = res.Hash // don't retry the same bad content every interval
		}
		return
	}
	if res.Changed {
		h.configHash = res.Hash
		h.log.Printf("NinjaOne: applied settings from field %s: %s", res.Field, strings.Join(res.Applied, ", "))
	}
}

// pushNinja updates the custom fields when the report changed or the push
// interval has passed.
func (h *handler) pushNinja(cfg config.Config, r status.Report, now time.Time) {
	cli := h.ninjaCLI(cfg)
	if cli == nil {
		return
	}
	fp := status.Fingerprint(r)
	if fp == h.lastFingerprint && !h.lastPush.IsZero() && now.Sub(h.lastPush) < cfg.NinjaPush() {
		return
	}
	if now.Before(h.pushNotBefore) {
		return // backing off after an error
	}
	res, err := PushReport(context.Background(), cli, cfg, r)
	if err != nil {
		if !h.ninjaErrLogged {
			h.log.Printf("NinjaOne: %v (retrying every %s)", err, pushRetry)
			h.ninjaErrLogged = true
		}
		// Don't record the fingerprint, so the fields that failed are retried
		// after a pause rather than on every watchdog tick.
		h.lastPush, h.pushNotBefore = now, now.Add(pushRetry)
		return
	}
	if h.ninjaErrLogged {
		h.log.Printf("NinjaOne: reporting works again")
		h.ninjaErrLogged = false
	}
	if fp != h.lastFingerprint {
		h.log.Printf("NinjaOne: updated %d field(s): health %s, %d folder(s) not syncing", len(res.Fields), r.Health, r.StaleFolderCount)
	}
	h.lastFingerprint, h.lastPush = fp, now
}
