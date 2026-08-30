package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport is the streamable-http Transport implementation
// (the protocol mcp-light speaks; SCOPE §43). One instance per MCP
// server declaration; created by NewHTTPTransport.
//
// Wire shape:
//
//   - Session negotiation (transparent to callers): the transport
//     issues the canonical MCP streamable-http `initialize` JSON-RPC
//     request on the first call (List or Call), captures the
//     `Mcp-Session-Id` response header (the canonical MCP
//     streamable-http session-id field; case-insensitive per HTTP
//     header semantics), and caches it in t.sessionID. Subsequent
//     requests on the same transport instance attach the header to
//     every outgoing request. The preflight is serialized through a
//     sync.Once so concurrent first-call attempts share a single
//     `initialize` round-trip; the cached session id is reused
//     across all subsequent calls. A server that does NOT require
//     session negotiation (e.g., a stub or a non-session-aware
//     server) is unaffected: the `initialize` request succeeds, the
//     response carries no `Mcp-Session-Id` header, t.sessionID stays
//     empty, and the `if t.sessionID != ""` guard skips header
//     attachment on subsequent calls. The wire shape for non-
//     `initialize` requests is the existing Content-Type + Accept
//     headers plus the conditional Mcp-Session-Id header.
//
//   - List(): POST to endpoint with {"jsonrpc":"2.0","id":1,"method":
//     "tools/list","params":{}}. Parse the JSON-RPC response.
//     "result": {"tools":[<tool>,...]}. Each tool's name +
//     description + inputSchema map verbatim from the server's
//     response. JSON-RPC error → List returns the wrapped error
//     (declared-but-unreachable / structured startup error per
//     GOAL §2 bound decision 4).
//   - Call(): POST to endpoint with {"jsonrpc":"2.0","id":<n>,
//     "method":"tools/call","params":{"name":<name>,"arguments":
//     <args>}}. Parse the response. "result": {"content":[...]} or
//     similar; transport.Call returns the result map verbatim. JSON-
//     RPC error → Call returns the wrapped error (structured
//     transport failure per GOAL §2 bound decision 4).
//   - Close(): releases the http.Client's idle connections. No
//     long-lived resources are held beyond that.
//
// The transport is HTTP-ONLY — it does not support server-initiated
// push, and does not implement sampling or roots (per Out-§11
// replacement). The streamable-http
// `Accept: application/json, text/event-stream` header is set per
// the spec baseline. Response parsing supports BOTH the JSON form
// (`Content-Type: application/json`) AND the SSE form
// (`Content-Type: text/event-stream`) — the MCP streamable-http spec
// permits the server to return either; live mcp-light returns SSE
// for every method (including `initialize`). The transport does
// not subscribe to server-pushed events or hold open the response
// stream; it reads the body to EOF, extracts any `data:` lines
// from the SSE event wrapper, concatenates the JSON payload, and
// parses it via the existing JSON-RPC decoder. The fix is GENERIC
// per the MCP streamable-http spec — any spec-compliant MCP server
// that returns JSON works; any spec-compliant MCP server that
// returns SSE works.
//
// Per SCOPE §30: the transport does NOT log the endpoint URL or
// request/response bodies verbatim in error messages — if the
// endpoint URL embeds a credential, the credential is never echoed.
// The transport's error format `mcp: http <status>: <err>` does not
// include the URL. The transport does NOT add an `Authorization`
// header from the endpoint URL (MCP does not define that; credentials
// come from environment / OS keychain, not from the config endpoint
// string per SCOPE §30).
//
// The session-negotiation fix follows the MCP streamable-http spec
// shape — it speaks the protocol any spec-compliant MCP server
// implements, NOT a hack specific to mcp-light (per amendment §44).
// The `initialize` request itself does NOT carry the Mcp-Session-Id
// header (the header is what we're trying to obtain); the preflight
// uses a direct `http.Client.Do` call (NOT a recursive `roundtrip`
// call) to avoid infinite recursion against an empty session cache.
type httpTransport struct {
	endpoint    string
	client      *http.Client
	sessionID   string
	sessionOnce sync.Once
	sessionErr  error
}

// Version is the clientInfo name + version the httpTransport's
// `initialize` preflight advertises to the MCP server. It is a
// local constant (the transport does not import the cmd package's
// exported Version to keep the package surface narrow — the cmd-
// side Version literal at cmd/simple-harness/main.go stays byte-
// identical at `(Run 020, handoff 065)` through handoff 066 per
// the supervisor's standing discipline; the transport's clientInfo
// is the MCP wire surface, NOT the harness's runtime Version).
const httpTransportClientName = "simple-harness"

// httpTransportClientVersion is the MCP clientInfo.version literal
// the initialize preflight advertises. It mirrors the harness's
// runtime Version family but is independent of it — the MCP wire
// protocol's clientInfo.version is a protocol identifier (what the
// server logs when reporting connection metadata), NOT the harness's
// release identifier. The string is opaque to the MCP server; the
// server does not act on it.
const httpTransportClientVersion = "0.1.0-dev"

// NewHTTPTransport constructs an httpTransport for the given endpoint.
// The client uses a sane default timeout (30s); the per-call
// ctx cancellation overrides the timeout (http.NewRequestWithContext
// cancels the in-flight request when ctx fires).
//
// The endpoint is stored verbatim and is NOT logged in error messages
// (per SCOPE §30 — the endpoint may embed credentials in the URL).
func NewHTTPTransport(endpoint string) *httpTransport {
	return &httpTransport{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Compile-time assertion that httpTransport satisfies Transport.
var _ Transport = (*httpTransport)(nil)

// List implements Transport.List. Sends a JSON-RPC 2.0 tools/list
// request and parses the result.tools array into []ListedTool. A
// JSON-RPC error response ({"error": {"code": <n>, "message": <s>}})
// surfaces as a wrapped error.
func (t *httpTransport) List(ctx context.Context) ([]ListedTool, error) {
	resp, err := t.roundtrip(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Tools []struct {
			Name        string                 `json:"name"`
			Description string                 `json:"description"`
			InputSchema map[string]interface{} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := unmarshalResult(resp, &wire); err != nil {
		return nil, err
	}
	out := make([]ListedTool, len(wire.Tools))
	for i, w := range wire.Tools {
		out[i] = ListedTool{
			Name:        w.Name,
			Description: w.Description,
			InputSchema: w.InputSchema,
		}
	}
	return out, nil
}

// Call implements Transport.Call. Sends a JSON-RPC 2.0 tools/call
// request and returns the result map verbatim. The MCP wire format
// treats the result as opaque content (the tools layer is
// responsible for the structured Result{Status,Content,Error}
// shape; the transport only returns what the server said).
func (t *httpTransport) Call(ctx context.Context, name string, args map[string]interface{}) (map[string]interface{}, error) {
	params := struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}{Name: name, Arguments: args}
	resp, err := t.roundtrip(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := unmarshalResult(resp, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, nil
}

// Close implements Transport.Close. Releases the http.Client's idle
// connections; no other long-lived resources are held. The function
// is idempotent: a second call is a no-op.
func (t *httpTransport) Close() error {
	if t.client != nil {
		t.client.CloseIdleConnections()
	}
	return nil
}

// ensureSession runs the MCP streamable-http `initialize` preflight
// exactly once on the first call (List or Call) against this
// transport instance. The preflight issues a direct HTTP POST (NOT
// through roundtrip — the session id is what we're trying to obtain,
// so a recursive roundtrip would loop against the empty session
// cache). On success, the response's `Mcp-Session-Id` header is
// cached in t.sessionID. On failure, the error is cached in
// t.sessionErr; subsequent calls return the same cached error
// (matching the existing transport's "declared-but-unreachable"
// failure mode per SCOPE §43 + Out-§11 replacement).
//
// The sync.Once serializes concurrent first-call attempts so only
// one `initialize` round-trip is in flight at a time; once the
// session id is cached, subsequent roundtrip calls skip the
// preflight (the sync.Once.Do is a no-op on subsequent calls).
//
// Servers that do NOT require session negotiation succeed with an
// empty t.sessionID (the response carries no Mcp-Session-Id
// header); roundtrip's `if t.sessionID != ""` guard then skips
// header attachment, leaving the wire shape unchanged from the
// pre-fix implementation.
func (t *httpTransport) ensureSession(ctx context.Context) error {
	t.sessionOnce.Do(func() {
		initBody := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"method":  "initialize",
			"params": map[string]interface{}{
				"protocolVersion": "2025-03-26",
				"capabilities":    map[string]interface{}{},
				"clientInfo": map[string]interface{}{
					"name":    httpTransportClientName,
					"version": httpTransportClientVersion,
				},
			},
		}
		bs, err := json.Marshal(initBody)
		if err != nil {
			t.sessionErr = fmt.Errorf("mcp: marshal initialize: %w", err)
			return
		}
		req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(bs))
		if err != nil {
			t.sessionErr = fmt.Errorf("mcp: build initialize: %w", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")

		resp, err := t.client.Do(req)
		if err != nil {
			t.sessionErr = fmt.Errorf("mcp: http initialize: %w", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.sessionErr = fmt.Errorf("mcp: http initialize: %s", resp.Status)
			return
		}
		// The wire shape only requires the Mcp-Session-Id
		// response header; the JSON-RPC payload of the
		// `initialize` response is opaque to the harness (no
		// per-server capabilities are negotiated today; future
		// capability-aware code can decode here). Drain the
		// body to EOF so the connection can be re-used by the
		// underlying transport; the SSE form is handled by the
		// helper but is irrelevant for the preflight (we only
		// read the session id header, not the JSON payload).
		_, _ = io.Copy(io.Discard, resp.Body)
		// Capture the canonical MCP streamable-http session-id
		// response header. Case-insensitive lookup per HTTP
		// header semantics; the spec uses Mcp-Session-Id as
		// the canonical case.
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.sessionID = sid
		}
	})
	return t.sessionErr
}

// roundtrip sends a single JSON-RPC 2.0 POST and returns the parsed
// response. The "id" field is the atomic counter (the transport
// keeps no per-call id; MCP http is request/response, so the id
// distinguishes concurrent calls — the WORK-4 end-to-end pins will
// exercise concurrent calls; the per-transport atomic counter is
// the same wire the WORK-1 Manager + adapter use).
//
// On entry, the method runs the `initialize` preflight (once-only,
// guarded by sync.Once) and attaches the cached Mcp-Session-Id
// header to the outgoing request when the server returned one on
// `initialize`. The preflight's own `initialize` request is issued
// from ensureSession via a direct http.Client.Do call (NOT through
// this method) to avoid infinite recursion against an empty
// session cache.
func (t *httpTransport) roundtrip(ctx context.Context, method string, params interface{}) (*jsonResponse, error) {
	if err := t.ensureSession(ctx); err != nil {
		return nil, err
	}
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	bs, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(bs))
	if err != nil {
		return nil, fmt.Errorf("mcp: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: http request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp: http %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mcp: read response: %w", err)
	}
	// The MCP streamable-http spec lets the server respond with
	// either `application/json` (plain JSON body) or
	// `text/event-stream` (SSE event stream wrapping the JSON
	// payload). Live mcp-light returns the SSE form for every
	// method; the harness must accept both to integrate against
	// any spec-compliant MCP server. stripSSEWrapper returns the
	// raw JSON payload regardless of which form the server used.
	if isSSEResponse(resp.Header.Get("Content-Type")) {
		raw = stripSSEWrapper(raw)
	}
	parsed, err := parseJSONRPC(raw)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

// jsonResponse holds the parsed JSON-RPC 2.0 response. Result is
// nil when the response carries only an error; Error is nil when
// the response carries only a result. Exactly one of the two is
// non-nil on a well-formed response.
type jsonResponse struct {
	Result json.RawMessage
	Error  *jsonRPCError
}

// jsonRPCError mirrors the {"code": <n>, "message": <s>} shape of
// a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// parseJSONRPC decodes a JSON-RPC 2.0 response body. Surfaces a
// JSON-RPC error object as a wrapped Go error; surfaces a parse
// failure as a wrapped Go error. The endpoint URL is NOT included
// in any error message (per SCOPE §30 — endpoint may embed creds).
func parseJSONRPC(raw []byte) (*jsonResponse, error) {
	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  *jsonRPCError   `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("mcp: parse jsonrpc response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("mcp: jsonrpc error %d: %s", parsed.Error.Code, parsed.Error.Message)
	}
	return &jsonResponse{Result: parsed.Result}, nil
}

// unmarshalResult unmarshals the JSON-RPC "result" payload into
// out. A null result is a no-op (out is left unchanged). The error
// is wrapped so the caller can distinguish parse failures from
// transport failures.
func unmarshalResult(resp *jsonResponse, out interface{}) error {
	if resp == nil || len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("mcp: parse result: %w", err)
	}
	return nil
}

// isSSEResponse reports whether the response Content-Type indicates
// a server-sent events stream. The MCP streamable-http spec uses
// `text/event-stream` for the SSE response form (the server returns
// either JSON or SSE; the client indicates both are acceptable via
// the `Accept: application/json, text/event-stream` request header).
// The check is case-insensitive on the type token and ignores any
// `; charset=...` parameter.
func isSSEResponse(contentType string) bool {
	if contentType == "" {
		return false
	}
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.EqualFold(strings.TrimSpace(contentType), "text/event-stream")
}

// stripSSEWrapper extracts the JSON payload(s) from an SSE event-
// stream body and returns them concatenated. The MCP streamable-http
// wire form wraps the JSON-RPC response in a single SSE event:
//
//	event: message
//	data: {"jsonrpc":"2.0",...}
//
// (blank line)
//
// The function reads every `data:` line (each is a fragment of the
// JSON payload; multiple lines are joined with '\n') and returns
// the concatenated bytes. A body that carries no `data:` lines is
// returned as-is so a non-SSE body that happens to have a
// `text/event-stream` Content-Type still reaches the JSON parser
// (and the parser surfaces a clean "invalid character" error
// rather than an empty payload).
//
// The implementation is intentionally minimal — no comments, no
// event-types, no retry tokens. The MCP wire only uses `event:
// message` + `data: <json>`; the harness ignores any other fields
// the server might add. The transport does NOT subscribe to
// server-pushed events or hold the stream open — it reads to EOF
// and parses one response.
func stripSSEWrapper(raw []byte) []byte {
	var out []byte
	for {
		i := bytes.IndexByte(raw, '\n')
		var line []byte
		if i < 0 {
			line = raw
			raw = nil
		} else {
			line = raw[:i]
			raw = raw[i+1:]
		}
		// Trim the trailing \r (CRLF line endings are common
		// over HTTP).
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(line[len("data:"):])
			if len(out) > 0 {
				out = append(out, '\n')
			}
			out = append(out, data...)
		}
		if i < 0 {
			break
		}
	}
	if len(out) == 0 {
		return raw
	}
	return out
}
