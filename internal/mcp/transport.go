// Package mcp is the Model Context Protocol client of Simple Harness:
// configuration-pinned servers are listed once at session start and
// their tools registered into the shared tools.Registry behind
// adapters that go through the same schema → path → policy pipeline
// as the builtins.
//
// The Transport interface is declared in types.go; transport_http.go
// (streamable HTTP, the protocol mcp-light speaks) and
// transport_stdio.go (a child process on stdio, under SCOPE §27's
// process-group discipline) implement it. Both perform the MCP
// initialize exchange before any other request.
package mcp
