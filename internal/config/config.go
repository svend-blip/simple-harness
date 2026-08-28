// Package config resolves the Simple Harness configuration from the
// precedence chain described in SCOPE §29 (defaults → user config →
// project config → environment variables) and renders the resolved
// configuration for operator inspection via the `config show` verb.
//
// This package READS configuration; it does not act on it. Callers
// (internal/model, internal/loop) treat the returned Config as
// immutable input data. See docs/ARCHITECTURE.md §"internal/config/".
//
// Architectural boundary: this is a Simple Harness component. It does
// not import orchestration, harness selection, GPU/VRAM allocation,
// model lifecycle, or Model Allocator policy.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the resolved configuration after the precedence chain has
// been applied. The fields below are the V1 minimum enumerated in
// GOAL §3 (config core) and SCOPE §29 (configuration). New fields
// land with their owning component in a later Run.
type Config struct {
	Model ModelConfig `json:"model"`
}

// ModelConfig is the OpenAI-compatible endpoint configuration.
// SCOPE §6 (OpenAI-compatible model interface) names these fields;
// SCOPE §29's example shows the same nested shape under a "model:" key.
// The JSON tags match SCOPE §29's YAML keys 1:1.
//
// The Go field name `Model` reads awkwardly as `mc.Model` but matches
// the SCOPE example verbatim and keeps the JSON key 1:1 — see
// handoff 006 §1 (config struct) for the readability cost decision.
type ModelConfig struct {
	Provider        string        `json:"provider"`
	BaseURL         string        `json:"base_url"`
	Model           string        `json:"model"`
	APIKey          string        `json:"api_key"`
	Temperature     float64       `json:"temperature"`
	MaxOutputTokens int           `json:"max_output_tokens"`
	RequestTimeout  time.Duration `json:"request_timeout"`
}

// Default returns the frozen default configuration. The defaults match
// SCOPE §29's example block plus a V1 default for request_timeout
// (30s). RequestTimeout is a time.Duration; the JSON unmarshaller
// parses strings ("30s", "1m", "500ms") via time.ParseDuration and
// accepts integers as seconds.
func Default() Config {
	return Config{
		Model: ModelConfig{
			Provider:        "openai_compatible",
			BaseURL:         "http://127.0.0.1:8080/v1",
			Model:           "qwen",
			APIKey:          "",
			Temperature:     0.2,
			MaxOutputTokens: 8192,
			RequestTimeout:  30 * time.Second,
		},
	}
}

// Load resolves the configuration from the real filesystem and
// environment. Use this from cmd/simple-harness/main.go's runConfig
// helper. Use loadFrom(...) from tests for isolation.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("cannot determine HOME: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("cannot determine cwd: %w", err)
	}
	return loadFrom(home, cwd, os.Environ())
}

// loadFrom resolves the configuration from explicit paths and an
// explicit env, in this order:
//
//	defaults   ← Default()
//	    ↓
//	user config (~/.simple-harness/config.json; missing → skip)
//	    ↓
//	project config (.simple-harness/config.json, searched upward
//	                from projectRoot to /; missing → skip)
//	    ↓
//	environment variables (SIMPLE_HARNESS_<FIELD>; missing → skip)
//
// A later source's value OVERRIDES the earlier one for any field it
// sets. A missing source is not an error; a malformed source IS an
// error. The function is the testable inner entry point; Load() is a
// thin wrapper that supplies the real home dir, project root, and env.
func loadFrom(homeDir, projectRoot string, env []string) (Config, error) {
	cfg := Default()

	// User config — skip silently if absent.
	userPath := filepath.Join(homeDir, ".simple-harness", "config.json")
	if err := applyFile(&cfg, userPath); err != nil {
		return cfg, err
	}

	// Project config — search upward from projectRoot for
	// .simple-harness/config.json. First match wins; missing at every
	// level is not an error.
	if projectPath, ok := findProjectConfig(projectRoot); ok {
		if err := applyFile(&cfg, projectPath); err != nil {
			return cfg, err
		}
	}

	// Environment variables — SIMPLE_HARNESS_<FIELD>.
	if err := applyEnv(&cfg, env); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// applyFile reads the JSON file at path (if it exists) and unmarshals
// it into a pointer-overlay struct, then applies non-nil fields on top
// of *cfg. Missing files are silently skipped; malformed JSON or an
// unparseable field value (e.g. request_timeout that doesn't parse as
// a Go duration) is an error.
//
// The pointer-overlay pattern disambiguates "field unset in the file"
// (leave the prior value alone) from "field set to the zero value"
// (replace the prior value). Without it, an `api_key: ""` line in the
// user config would be impossible to distinguish from "no api_key line
// at all". A second pre-decode pass builds a presence map for the
// "model" object so explicit JSON null can be distinguished from
// absent — `encoding/json` alone cannot do this through `*T` pointers
// (a null sets the pointer to nil, same as an absent key). See
// presenceSetFor and applyOverlay for the three-case (absent /
// present-non-null / present-null) handling.
func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	modelPresent, err := presenceSetFor(data, "model")
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	var overlay configOverlay
	if err := json.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := applyOverlay(cfg, overlay, modelPresent); err != nil {
		return fmt.Errorf("apply %s: %w", path, err)
	}
	return nil
}

// presenceSetFor returns the set of top-level keys present inside the
// JSON object named by key in data. Present-but-null keys are included;
// absent keys are not. It is used to disambiguate "field explicitly
// null" from "field absent" — `encoding/json` cannot do this through
// `*T` pointer fields alone (a null sets the pointer to nil, same as
// an absent key), so a pre-decode pass into map[string]json.RawMessage
// is required. If key itself is absent from data, the returned map is
// empty (not an error); a malformed JSON value at key returns an error.
func presenceSetFor(data []byte, key string) (map[string]struct{}, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	inner, ok := raw[key]
	if !ok {
		return map[string]struct{}{}, nil
	}
	var imap map[string]json.RawMessage
	if err := json.Unmarshal(inner, &imap); err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(imap))
	for k := range imap {
		out[k] = struct{}{}
	}
	return out, nil
}

// findProjectConfig walks upward from projectRoot looking for
// .simple-harness/config.json. It returns the path of the first match
// and true; or "" and false if no match is found before reaching the
// filesystem root.
//
// Permission errors at upper levels are treated as "stop walking, no
// project config" rather than as loader errors: the loader must be
// robust against unreadable ancestor directories on shared systems.
func findProjectConfig(projectRoot string) (string, bool) {
	dir := projectRoot
	for {
		candidate := filepath.Join(dir, ".simple-harness", "config.json")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// applyEnv scans env for SIMPLE_HARNESS_<FIELD> keys and applies them
// on top of *cfg. Unparseable values are wrapped with the field name
// so the operator can see which env var misbehaved.
func applyEnv(cfg *Config, env []string) error {
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, "SIMPLE_HARNESS_") {
			continue
		}
		field := strings.ToLower(strings.TrimPrefix(key, "SIMPLE_HARNESS_"))
		if err := setEnvField(cfg, field, val); err != nil {
			return fmt.Errorf("env %s: %w", key, err)
		}
	}
	return nil
}

// setEnvField applies a single env value to the matching field on
// cfg.Model. Field name is the lowercased suffix (e.g. "base_url").
func setEnvField(cfg *Config, field, val string) error {
	mc := &cfg.Model
	switch field {
	case "provider":
		mc.Provider = val
	case "base_url":
		mc.BaseURL = val
	case "model":
		mc.Model = val
	case "api_key":
		mc.APIKey = val
	case "temperature":
		var f float64
		if _, err := fmt.Sscanf(val, "%f", &f); err != nil {
			return fmt.Errorf("invalid temperature %q: %w", val, err)
		}
		mc.Temperature = f
	case "max_output_tokens":
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
			return fmt.Errorf("invalid max_output_tokens %q: %w", val, err)
		}
		mc.MaxOutputTokens = n
	case "request_timeout":
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid request_timeout %q: %w", val, err)
		}
		mc.RequestTimeout = d
	default:
		// Unknown SIMPLE_HARNESS_* env vars are silently ignored so
		// future fields can land without breaking older binaries.
		_ = val
	}
	return nil
}

// Render writes the resolved configuration to w in a deterministic,
// machine-parseable format (JSON), with secret fields redacted per
// SCOPE §30. The api_key field is replaced with "<redacted>" when
// non-empty; an empty api_key is rendered as the empty string so the
// operator can tell whether a key is set or not without exposing the
// value.
//
// The shadow-copy pattern keeps the actual Config value immutable
// while producing a redacted view for output. renderView is the
// marshalling shape: it mirrors Config but renders request_timeout as
// a human-readable string ("30s") instead of nanoseconds.
func (c Config) Render(w io.Writer) error {
	shadow := c
	if shadow.Model.APIKey != "" {
		shadow.Model.APIKey = "<redacted>"
	}
	v := renderView{Model: renderModelView{
		Provider:        shadow.Model.Provider,
		BaseURL:         shadow.Model.BaseURL,
		Model:           shadow.Model.Model,
		APIKey:          shadow.Model.APIKey,
		Temperature:     shadow.Model.Temperature,
		MaxOutputTokens: shadow.Model.MaxOutputTokens,
		RequestTimeout:  shadow.Model.RequestTimeout.String(),
	}}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// renderView is the JSON marshalling shape for Render output. It
// mirrors Config but uses string for request_timeout so the output is
// human-readable.
type renderView struct {
	Model renderModelView `json:"model"`
}

// renderModelView mirrors ModelConfig for marshalling only.
type renderModelView struct {
	Provider        string  `json:"provider"`
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	APIKey          string  `json:"api_key"`
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	RequestTimeout  string  `json:"request_timeout"`
}

// --- pointer-overlay types -------------------------------------------------

// configOverlay is the JSON unmarshalling target for an on-disk
// config file. Every field is a pointer so the loader can distinguish
// "field unset in the file" (nil → leave prior value alone) from
// "field set to the zero value" (non-nil → replace prior value).
type configOverlay struct {
	Model *modelOverlay `json:"model"`
}

type modelOverlay struct {
	Provider        *string  `json:"provider"`
	BaseURL         *string  `json:"base_url"`
	Model           *string  `json:"model"`
	APIKey          *string  `json:"api_key"`
	Temperature     *float64 `json:"temperature"`
	MaxOutputTokens *int     `json:"max_output_tokens"`
	RequestTimeout  *string  `json:"request_timeout"`
}

// applyOverlay merges a non-nil overlay on top of *cfg. Each field on
// the inner model object honours the three-case contract per SCOPE
// §29 strict-override intent:
//   - present, non-nil pointer → apply real value (replace prior value)
//   - present, nil pointer → explicit JSON null → zero out (clear prior value)
//   - absent → leave prior value alone
//
// The presence map for the model object is built by presenceSetFor in
// applyFile and passed in here; it is the only way to tell
// "present-but-null" apart from "absent" using stdlib `encoding/json`
// against `*T` pointer fields. The duration is parsed here (not via a
// custom UnmarshalJSON on a wrapper type) so a parse-failure error
// message can name the field it came from — the Go json package does
// not auto-wrap custom UnmarshalJSON errors with the field path.
func applyOverlay(cfg *Config, overlay configOverlay, modelPresent map[string]struct{}) error {
	if overlay.Model == nil {
		return nil
	}
	m := overlay.Model
	mc := &cfg.Model
	if m.Provider != nil {
		mc.Provider = *m.Provider
	} else if _, ok := modelPresent["provider"]; ok {
		mc.Provider = ""
	}
	if m.BaseURL != nil {
		mc.BaseURL = *m.BaseURL
	} else if _, ok := modelPresent["base_url"]; ok {
		mc.BaseURL = ""
	}
	if m.Model != nil {
		mc.Model = *m.Model
	} else if _, ok := modelPresent["model"]; ok {
		mc.Model = ""
	}
	if m.APIKey != nil {
		mc.APIKey = *m.APIKey
	} else if _, ok := modelPresent["api_key"]; ok {
		mc.APIKey = ""
	}
	if m.Temperature != nil {
		mc.Temperature = *m.Temperature
	} else if _, ok := modelPresent["temperature"]; ok {
		mc.Temperature = 0
	}
	if m.MaxOutputTokens != nil {
		mc.MaxOutputTokens = *m.MaxOutputTokens
	} else if _, ok := modelPresent["max_output_tokens"]; ok {
		mc.MaxOutputTokens = 0
	}
	if m.RequestTimeout != nil {
		dur, err := parseDurationField(*m.RequestTimeout, "request_timeout")
		if err != nil {
			return err
		}
		mc.RequestTimeout = dur
	} else if _, ok := modelPresent["request_timeout"]; ok {
		mc.RequestTimeout = 0
	}
	return nil
}

// parseDurationField parses a JSON duration string ("30s", "1m",
// "500ms") or a numeric literal (treated as seconds) and returns the
// resulting time.Duration. fieldName is included in the error so the
// operator can see which field was unparseable.
func parseDurationField(s, fieldName string) (time.Duration, error) {
	// Try parsing as a Go duration string first (handles "30s",
	// "1m500ms", "2h30m", etc.).
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Fall back to a numeric value (treated as seconds). This keeps
	// the loader forgiving for configs that store 30 instead of "30s".
	var secs float64
	if _, err := fmt.Sscanf(s, "%f", &secs); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid %s %q (expected Go duration string or numeric seconds)", fieldName, s)
}
