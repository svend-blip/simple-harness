package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpTransport is the streamable-http Transport implementation
// (the protocol mcp-light speaks; SCOPE §43). One instance per MCP
// server declaration; created by NewHTTPTransport.
//
// Wire shape:
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
// The transport is HTTP-ONLY — it does not parse SSE streams, does
// not support server-initiated push, and does not implement sampling
// or roots (per Out-§11 replacement). The streamable-http
// `Accept: application/json, text/event-stream` header is set per
// the spec baseline; the WORK 4 end-to-end tests against mcp-light
// will exercise the SSE form if the live server offers it. For THIS
// handoff the wire is plain JSON response.
//
// Per SCOPE §30: the transport does NOT log the endpoint URL or
// request/response bodies verbatim in error messages — if the
// endpoint URL embeds a credential, the credential is never echoed.
// The transport's error format `mcp: http <status>: <err>` does not
// include the URL. The transport does NOT add an `Authorization`
// header from the endpoint URL (MCP does not define that; credentials
// come from environment / OS keychain, not from the config endpoint
// string per SCOPE §30).
type httpTransport struct {
	endpoint string
	client   *http.Client
}

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

// roundtrip sends a single JSON-RPC 2.0 POST and returns the parsed
// response. The "id" field is the atomic counter (the transport
// keeps no per-call id; MCP http is request/response, so the id
// distinguishes concurrent calls — the WORK-4 end-to-end pins will
// exercise concurrent calls; the per-transport atomic counter is
// the same wire the WORK-1 Manager + adapter use).
func (t *httpTransport) roundtrip(ctx context.Context, method string, params interface{}) (*jsonResponse, error) {
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
