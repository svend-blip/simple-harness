package mcp

import (
	"fmt"
	"strings"

	"github.com/svend-blip/simple-harness/internal/tools"
)

// ResolveFinalName returns the registration name for an MCP tool
// against the given registry. The rule is deterministic and binding
// per SCOPE §43 + Run 019 GOAL §2 bound decision 5:
//
//   - If the original name does NOT collide with any builtin already
//     registered in the given registry, the original name is returned
//     (the MCP tool surfaces under the server's reported name).
//   - If the original name DOES collide with a builtin, "<server>__
//     <original>" is returned (the builtin wins; the MCP tool is
//     surfaced under the deterministic prefix form).
//
// No silent shadowing in either direction: the builtin stays under its
// own name, and the MCP tool is registered under the documented prefix
// form. TestMCP_BuildToolListing_CollisionNaming (registry_test.go)
// pins this contract end-to-end through Manager.AddServer; the unit
// coverage here is for the function alone.
//
// The serverName portion is sanitized (any "__" sequence becomes "_")
// to keep the <server>__<tool> prefix unambiguous: a server named
// "foo__bar" stays "foo_bar" in the prefix, so the first "__" in the
// registration name always marks the server/tool separator. The
// resolve is the single source of truth for the prefix form; other
// call sites (context diagnostics in WORK 4) MUST go through this
// function (or call sanitizeServerName directly) to stay consistent.
//
// Concurrency: registry.Names takes the registry's RWMutex read-side
// (safe for concurrent reads after construction). The function is
// called at registration time only (AddServer is single-threaded), so
// concurrency is a non-issue in practice; the lock is held for the
// short duration of the lookup.
func ResolveFinalName(registry *tools.Registry, serverName, originalName string) string {
	if !registryHasName(registry, originalName) {
		return originalName
	}
	return FormatCollisionName(serverName, originalName)
}

// FormatCollisionName returns the deterministic collision-naming form
// "<server>__<tool>" (double underscore; server name sanitized). The
// function is the single source of truth for the prefix shape — any
// other call site that needs the collision form (diagnostics, the
// `simple-harness tools` listing in WORK 4) should call this function
// rather than constructing the form inline. Keeping the form in one
// place is what makes the collision rule auditable.
func FormatCollisionName(serverName, originalName string) string {
	return fmt.Sprintf("%s__%s", sanitizeServerName(serverName), originalName)
}

// registryHasName reports whether the registry contains the given
// tool name. Linear scan over registry.Names (already sorted); the
// listing is small (handful of tools) so the linear scan is fine. A
// future Run could expose a registry.Has(name) helper if the listing
// grows.
func registryHasName(registry *tools.Registry, name string) bool {
	for _, n := range registry.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// sanitizeServerName replaces any "__" sequence in the server name
// with "_". This keeps the <server>__<tool> prefix unambiguous: a
// server named "foo__bar" becomes "foo_bar" so the first "__" in the
// registration name always marks the server/tool separator.
//
// Example: a server named "weather" stays "weather"; a server named
// "foo__bar" becomes "foo_bar" (the prefix is "<foo_bar>__<tool>",
// and splitting on the first "__" yields the server name "foo_bar").
func sanitizeServerName(s string) string {
	return strings.ReplaceAll(s, "__", "_")
}