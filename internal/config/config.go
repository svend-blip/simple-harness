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
// GOAL §3 (config core) and SCOPE §29 (configuration) plus the
// Run 019 WORK 3 amendment (SCOPE §43 + Out-§11 replacement).
//
// MCPServers is the resolved list of MCP server declarations
// configuration-pinned under the `mcp_servers` key. The slice is
// empty when no servers are declared; the harness treats "no servers"
// as "no MCP tools available". The cmd-side wiring in WORK 4
// converts each MCPServerConfig to an mcp.Server at session start;
// the config layer does NOT import internal/mcp/ (the two are
// decoupled per SCOPE §29's "small predictable configuration
// hierarchy" + the no-new-abstractions principle).
type Config struct {
	Model      ModelConfig       `json:"model"`
	MCPServers []MCPServerConfig `json:"mcp_servers,omitempty"`
	// ShellTimeout is the default deadline applied to a shell tool call
	// whose caller did not pass timeout_ms. Zero disables the default
	// (the pre-2026-09-02 behaviour: a call with no timeout_ms runs
	// until the process group exits). The default exists because a
	// model never sets timeout_ms on its own: measured 2026-09-02 on a
	// chain role, a shell call that backgrounded a helper and left the
	// pipe open waited 3 h 24 min. JSON key `shell_timeout`, env
	// SIMPLE_HARNESS_SHELL_TIMEOUT, Go duration syntax ("10m", "600s").
	ShellTimeout time.Duration `json:"shell_timeout"`
}

// MCPServerConfig is the resolved shape of one entry under the
// `mcp_servers` key. The JSON tags mirror SCOPE §43's configuration
// contract:
//
//   - server name (stable identifier)
//   - transport: stdio | http
//   - endpoint or command
//   - permission mode the server's tools map into
//   - optional tool allowlist (subset of what the server offers)
//
// Optional fields follow SCOPE §30's "credentials may be supplied
// using suitable mechanisms such as environment variables /
// configuration / CLI" — api_key (per-server authentication header
// value) and headers (per-server custom HTTP headers). Both are
// REDACTED in Render() output per SCOPE §30 binding ("Sensitive
// headers must be redacted"; "Secrets must not appear in normal
// startup output / JSONL events / session logs / context
// diagnostics / HTTP diagnostic dumps").
type MCPServerConfig struct {
	Name       string            `json:"name"`
	Transport  string            `json:"transport"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Permission string            `json:"permission,omitempty"`
	Allowlist  []string          `json:"allowlist,omitempty"`
	APIKey     string            `json:"api_key,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
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
		ShellTimeout: 10 * time.Minute,
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

	// Post-load validation of the mcp_servers slice. A misconfigured
	// server entry returns a structured error the caller maps to
	// exit 2 (WORK 4's cmd-side wiring). Validation runs AFTER the
	// precedence chain has merged every source so the operator
	// sees the final shape that will be passed to session start.
	if err := validateMCPServers(cfg.MCPServers); err != nil {
		return Config{}, err
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
	mcpServersPresent, err := presenceForTopKey(data, "mcp_servers")
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	var overlay configOverlay
	if err := json.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := applyOverlay(cfg, overlay, modelPresent, mcpServersPresent); err != nil {
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

// presenceForTopKey reports whether the top-level key is present in
// the JSON object in data. Present-but-null keys count as present.
// Returns (false, nil) for an absent key. The function is the
// coarse-grained analog of presenceSetFor for the `mcp_servers`
// array key — the array overlay does not need per-field
// null-vs-absent tracking (every entry is fully replaced when the
// key is present, matching SCOPE §43's "declarative pinned"
// shape), only the key-vs-absent distinction the `*[]mcpServerOverlay`
// pointer cannot make on its own.
func presenceForTopKey(data []byte, key string) (bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, err
	}
	_, ok := raw[key]
	return ok, nil
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
	case "shell_timeout":
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid shell_timeout %q: %w", val, err)
		}
		if d < 0 {
			return fmt.Errorf("invalid shell_timeout %q: must not be negative", val)
		}
		cfg.ShellTimeout = d
	default:
		// Unknown SIMPLE_HARNESS_* env vars are silently ignored so
		// future fields can land without breaking older binaries.
		_ = val
	}
	return nil
}

// validateMCPServers enforces the SCOPE §43 + §30 contract on each
// resolved MCPServerConfig entry. The rules:
//
//   - Name non-empty (the stable identifier; no MCP tool is
//     registerable without it).
//   - Transport is exactly "http" or "stdio" (case-sensitive; the
//     SCOPE §43 enumeration shape).
//   - For transport="http": Endpoint non-empty AND Command is empty
//     (a streamable-http server has an endpoint, no child-process
//     command).
//   - For transport="stdio": Command is non-empty (len ≥ 1) AND
//     Endpoint is empty (a stdio server's command is the canonical
//     address).
//   - Permission is one of "read_only" | "workspace_write" |
//     "full_access", or empty (the cmd-side maps empty to the
//     harness's active permission mode at session start).
//   - Allowlist, if non-empty, contains no empty strings.
//
// A failed validation returns an error of the form
//
//	mcp_servers[%d] %q: <rule>: %s
//
// where %d is the entry's position, %q is the entry's Name (if set),
// and <rule> names which contract clause failed. The caller
// (loadFrom) wraps the error with the source-file path so the operator
// can see exactly which entry + which source is misconfigured.
//
// The function does NOT touch internal/perm/ — the Permission string
// is validated against the same three mode literals the harness uses
// at runtime; the mapping to perm.NewPolicy(mode) is WORK 4's
// cmd-side wiring.
func validateMCPServers(servers []MCPServerConfig) error {
	for idx, srv := range servers {
		if srv.Name == "" {
			return fmt.Errorf("mcp_servers[%d]: %w", idx, errMCPServersNameRequired)
		}
		switch srv.Transport {
		case "http":
			if srv.Endpoint == "" {
				return fmt.Errorf("mcp_servers[%d] %q: %w", idx, srv.Name, errMCPServersEndpointRequired)
			}
			if len(srv.Command) > 0 {
				return fmt.Errorf("mcp_servers[%d] %q: %w", idx, srv.Name, errMCPServersHTTPNoCommand)
			}
		case "stdio":
			if len(srv.Command) == 0 {
				return fmt.Errorf("mcp_servers[%d] %q: %w", idx, srv.Name, errMCPServersCommandRequired)
			}
			if srv.Endpoint != "" {
				return fmt.Errorf("mcp_servers[%d] %q: %w", idx, srv.Name, errMCPServersStdioNoEndpoint)
			}
		case "":
			return fmt.Errorf("mcp_servers[%d] %q: %w", idx, srv.Name, errMCPServersTransportRequired)
		default:
			return fmt.Errorf("mcp_servers[%d] %q: %w (got %q)", idx, srv.Name, errMCPServersTransportInvalid, srv.Transport)
		}
		if srv.Permission != "" {
			switch srv.Permission {
			case "read_only", "workspace_write", "full_access":
				// ok
			default:
				return fmt.Errorf("mcp_servers[%d] %q: %w (got %q)", idx, srv.Name, errMCPServersPermissionInvalid, srv.Permission)
			}
		}
		for j, a := range srv.Allowlist {
			if a == "" {
				return fmt.Errorf("mcp_servers[%d] %q: %w (allowlist[%d] is empty)", idx, srv.Name, errMCPServersAllowlistEmpty, j)
			}
		}
	}
	return nil
}

// errMCPServers* are the sentinel errors validateMCPServers wraps.
// They carry a stable message ("") and are wrapped via %w so callers
// (and `errors.Is` checks at WORK 4) can identify the failure mode
// without string-matching the message.
var (
	errMCPServersNameRequired      = fmt.Errorf("name is required")
	errMCPServersTransportRequired = fmt.Errorf("transport is required (must be \"http\" or \"stdio\")")
	errMCPServersTransportInvalid  = fmt.Errorf("transport must be \"http\" or \"stdio\"")
	errMCPServersEndpointRequired  = fmt.Errorf("endpoint is required for transport \"http\"")
	errMCPServersHTTPNoCommand     = fmt.Errorf("command must be empty for transport \"http\"")
	errMCPServersCommandRequired   = fmt.Errorf("command is required for transport \"stdio\" (non-empty)")
	errMCPServersStdioNoEndpoint   = fmt.Errorf("endpoint must be empty for transport \"stdio\"")
	errMCPServersPermissionInvalid = fmt.Errorf("permission must be \"read_only\", \"workspace_write\", or \"full_access\" (empty inherits harness default)")
	errMCPServersAllowlistEmpty    = fmt.Errorf("allowlist entries must be non-empty strings")
)

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
//
// mcp_servers rendering follows SCOPE §30's redaction contract:
// api_key is replaced with "<redacted>" when non-empty, and each
// headers VALUE is replaced with "<redacted>" while keys stay visible
// (so the operator can see WHICH headers are configured). A nil
// MCPServers slice renders as the empty array "[]" so the operator
// can distinguish "zero servers declared" from "field absent" —
// both forms parse cleanly.
func (c Config) Render(w io.Writer) error {
	shadow := c
	if shadow.Model.APIKey != "" {
		shadow.Model.APIKey = "<redacted>"
	}
	mcpView := make([]renderMCPView, 0, len(shadow.MCPServers))
	for _, srv := range shadow.MCPServers {
		v := renderMCPView{
			Name:       srv.Name,
			Transport:  srv.Transport,
			Endpoint:   srv.Endpoint,
			Command:    srv.Command,
			Permission: srv.Permission,
			Allowlist:  srv.Allowlist,
		}
		if srv.APIKey != "" {
			v.APIKey = "<redacted>"
		}
		if len(srv.Headers) > 0 {
			v.Headers = make(map[string]string, len(srv.Headers))
			for k := range srv.Headers {
				v.Headers[k] = "<redacted>"
			}
		}
		mcpView = append(mcpView, v)
	}
	v := renderView{
		Model: renderModelView{
			Provider:        shadow.Model.Provider,
			BaseURL:         shadow.Model.BaseURL,
			Model:           shadow.Model.Model,
			APIKey:          shadow.Model.APIKey,
			Temperature:     shadow.Model.Temperature,
			MaxOutputTokens: shadow.Model.MaxOutputTokens,
			RequestTimeout:  shadow.Model.RequestTimeout.String(),
		},
		MCPServers:   mcpView,
		ShellTimeout: shadow.ShellTimeout.String(),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// renderView is the JSON marshalling shape for Render output. It
// mirrors Config but uses string for request_timeout so the output is
// human-readable.
type renderView struct {
	Model        renderModelView `json:"model"`
	MCPServers   []renderMCPView `json:"mcp_servers"`
	ShellTimeout string          `json:"shell_timeout"`
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

// renderMCPView mirrors MCPServerConfig for marshalling only, with
// the SCOPE §30 redaction baked into the field shape: APIKey carries
// "<redacted>" in place of the real value when non-empty; Headers'
// values are "<redacted>" while keys stay visible (so the operator
// can see WHICH headers are configured, per "Sensitive headers must
// be redacted" — keys are not secrets).
type renderMCPView struct {
	Name       string            `json:"name"`
	Transport  string            `json:"transport"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Permission string            `json:"permission,omitempty"`
	Allowlist  []string          `json:"allowlist,omitempty"`
	APIKey     string            `json:"api_key,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
}

// --- pointer-overlay types -------------------------------------------------

// configOverlay is the JSON unmarshalling target for an on-disk
// config file. Every field is a pointer so the loader can distinguish
// "field unset in the file" (nil → leave prior value alone) from
// "field set to the zero value" (non-nil → replace prior value).
type configOverlay struct {
	Model        *modelOverlay       `json:"model"`
	MCPServers   *[]mcpServerOverlay `json:"mcp_servers"`
	ShellTimeout *string             `json:"shell_timeout"`
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

// mcpServerOverlay is the per-field pointer-overlay shape for a
// single entry under the `mcp_servers` array. The pointer types
// mirror the modelOverlay discipline: a nil pointer means "absent
// in this entry" (the zero value of the resolved MCPServerConfig
// stays); a non-nil pointer means "set to this value" (the underlying
// string or slice is copied). The slice-level replacement is
// governed by the parent *[]mcpServerOverlay plus the
// mcpServersPresent flag from presenceForTopKey.
type mcpServerOverlay struct {
	Name       *string            `json:"name"`
	Transport  *string            `json:"transport"`
	Endpoint   *string            `json:"endpoint"`
	Command    *[]string          `json:"command"`
	Permission *string            `json:"permission"`
	Allowlist  *[]string          `json:"allowlist"`
	APIKey     *string            `json:"api_key"`
	Headers    *map[string]string `json:"headers"`
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
//
// The mcp_servers overlay follows an analogous three-case contract at
// the array level: present-with-value replaces the prior slice (each
// overlay entry fully materializes into an MCPServerConfig via
// applyMCPServerOverlay), present-but-null clears the prior slice,
// and absent leaves it alone. mcpServersPresent (a single bool from
// presenceForTopKey) is the array-level presence signal; the
// pointer-overlay per-field is sufficient to detect the
// present-with-value case.
func applyOverlay(cfg *Config, overlay configOverlay, modelPresent map[string]struct{}, mcpServersPresent bool) error {
	if overlay.ShellTimeout != nil {
		d, err := time.ParseDuration(*overlay.ShellTimeout)
		if err != nil {
			return fmt.Errorf("invalid shell_timeout %q: %w", *overlay.ShellTimeout, err)
		}
		if d < 0 {
			return fmt.Errorf("invalid shell_timeout %q: must not be negative", *overlay.ShellTimeout)
		}
		cfg.ShellTimeout = d
	}
	if overlay.Model == nil && overlay.MCPServers == nil && !mcpServersPresent {
		return nil
	}
	if overlay.Model != nil {
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
	}
	if overlay.MCPServers != nil {
		newServers := make([]MCPServerConfig, 0, len(*overlay.MCPServers))
		for _, srvOvr := range *overlay.MCPServers {
			newServers = append(newServers, applyMCPServerOverlay(srvOvr))
		}
		cfg.MCPServers = newServers
	} else if mcpServersPresent {
		cfg.MCPServers = nil
	}
	return nil
}

// applyMCPServerOverlay materializes one mcpServerOverlay into the
// resolved MCPServerConfig shape. The overlay's pointer-typed fields
// mean a nil pointer is "absent in this entry" (the zero value stays);
// a non-nil pointer is "set to this value" (the underlying string or
// slice is copied to keep the resolved Config independent of the
// parsed overlay).
//
// Per-field null-vs-absent tracking within an entry is intentionally
// not implemented: each source's array either replaces the prior
// value as a whole (overlay present) or leaves the prior value alone
// (overlay absent or null). Null-vs-absent within a single entry would
// require a per-entry presence map and is not required by SCOPE §43
// or the V1 amendment — it can be added in a later Run if a real
// server entry needs partial-overlay semantics.
func applyMCPServerOverlay(ovr mcpServerOverlay) MCPServerConfig {
	s := MCPServerConfig{}
	if ovr.Name != nil {
		s.Name = *ovr.Name
	}
	if ovr.Transport != nil {
		s.Transport = *ovr.Transport
	}
	if ovr.Endpoint != nil {
		s.Endpoint = *ovr.Endpoint
	}
	if ovr.Command != nil {
		s.Command = append([]string(nil), (*ovr.Command)...)
	}
	if ovr.Permission != nil {
		s.Permission = *ovr.Permission
	}
	if ovr.Allowlist != nil {
		s.Allowlist = append([]string(nil), (*ovr.Allowlist)...)
	}
	if ovr.APIKey != nil {
		s.APIKey = *ovr.APIKey
	}
	if ovr.Headers != nil {
		src := *ovr.Headers
		out := make(map[string]string, len(src))
		for k, v := range src {
			out[k] = v
		}
		s.Headers = out
	}
	return s
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
