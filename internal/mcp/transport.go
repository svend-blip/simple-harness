// Package mcp — Transport seam (WORK 2 lands the implementations).
//
// This file is intentionally minimal in the client-core handoff
// (Run 019 WORK slot 1, handoff 056): the Transport interface is
// declared in types.go (semantic alignment with the other public
// types; WORK 2 plugs production implementations behind the seam).
//
// WORK 2 (handoff 057) adds:
//
//   - internal/mcp/transport_http.go: streamable-http transport
//     (the protocol mcp-light speaks).
//   - internal/mcp/transport_stdio.go: stdio transport (MCP over
//     child-process stdio; SCOPE §27's process-group discipline
//     applies — Run 005's process-group ownership).
//
// Both implementations satisfy the Transport interface declared in
// types.go. The transport_stub_test.go file in this package provides
// the in-process stub the unit tests use today; the production
// transports replace the stub at WORK 2's close.
package mcp