package context

import (
	"strings"
	"testing"
)

// TestEstimate_KnownString pins the deterministic estimator
// behavior. "hello world" is 11 characters; (11+3)/4 = 3.
// The exact value is documented in a comment so the test is not
// tautological.
func TestEstimate_KnownString(t *testing.T) {
	got := Estimate("hello world")
	want := 3
	if got != want {
		t.Errorf("Estimate(%q) = %d, want %d", "hello world", got, want)
	}
}

func TestEstimate_EmptyString(t *testing.T) {
	got := Estimate("")
	if got != 0 {
		t.Errorf("Estimate(%q) = %d, want 0", "", got)
	}
}

func TestLedger_AddAndTotal(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "hhhh")
	l.Add(Task, "task", "tttttt")
	l.Add(Skill, "cold-start", "ssss")
	want := Estimate("hhhh") + Estimate("tttttt") + Estimate("ssss")
	if got := l.Total(); got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
}

func TestLedger_ByCategory(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "hhhh")
	l.Add(Task, "task", "tttttt")
	l.Add(Task, "task2", "tt")
	by := l.ByCategory()
	if len(by) != 2 {
		t.Fatalf("len(ByCategory) = %d, want 2 (got=%v)", len(by), by)
	}
	if by[HarnessSystem] != Estimate("hhhh") {
		t.Errorf("ByCategory[HarnessSystem] = %d, want %d", by[HarnessSystem], Estimate("hhhh"))
	}
	wantTask := Estimate("tttttt") + Estimate("tt")
	if by[Task] != wantTask {
		t.Errorf("ByCategory[Task] = %d, want %d", by[Task], wantTask)
	}
}

func TestLedger_Overflow_NoLimit(t *testing.T) {
	var l Ledger
	l.Add(Task, "task", strings.Repeat("a", 100000))
	if err := l.Overflow(); err != nil {
		t.Errorf("Overflow() with Limit=0 returned error: %v", err)
	}
}

func TestLedger_Overflow_Exceeds(t *testing.T) {
	l := Ledger{Limit: 10}
	l.Add(Task, "task", strings.Repeat("a", 100))
	err := l.Overflow()
	if err == nil {
		t.Fatal("Overflow() returned nil, want error")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Errorf("error message %q does not contain %q", err.Error(), "overflow")
	}
}

func TestLedger_Overflow_Within(t *testing.T) {
	l := Ledger{Limit: 10000}
	l.Add(Task, "task", "short")
	if err := l.Overflow(); err != nil {
		t.Errorf("Overflow() returned error within limit: %v", err)
	}
}

func TestLedger_Report_IncludesCategoriesAndTotal(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "harness-body")
	l.Add(Task, "task", "task-body")
	report := l.Report()
	for _, sub := range []string{"Total", "harness system", "task"} {
		if !strings.Contains(report, sub) {
			t.Errorf("Report() missing substring %q (report=%q)", sub, report)
		}
	}
}

func TestLedger_Doctor_LargeContributor_FindsByName(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", strings.Repeat("a", 5000))
	findings := l.Doctor()
	if len(findings) == 0 {
		t.Fatal("Doctor() returned no findings, want at least 1")
	}
	var found *Finding
	for i := range findings {
		if findings[i].Type == "large" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Doctor() returned no large findings (got=%+v)", findings)
	}
	if found.Category != HarnessSystem {
		t.Errorf("finding Category = %q, want %q", found.Category, HarnessSystem)
	}
	if found.Name != "harness" {
		t.Errorf("finding Name = %q, want %q", found.Name, "harness")
	}
	if !strings.Contains(found.Detail, "harness") {
		t.Errorf("finding Detail %q does not identify the contributor by name", found.Detail)
	}
}

func TestLedger_Doctor_Duplicates_Detected(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "DUPLICATED")
	l.Add(Task, "task", "DUPLICATED")
	findings := l.Doctor()
	var found *Finding
	for i := range findings {
		if findings[i].Type == "duplicate" {
			found = &findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Doctor() returned no duplicate findings (got=%+v)", findings)
	}
	if !strings.Contains(found.Detail, "2") {
		t.Errorf("duplicate Detail %q does not mention count of 2", found.Detail)
	}
}

func TestLedger_Doctor_NoFindings_CleanLedger(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "tiny")
	l.Add(Task, "task", "short")
	findings := l.Doctor()
	if len(findings) != 0 {
		t.Errorf("Doctor() returned %d findings on clean ledger, want 0 (got=%+v)", len(findings), findings)
	}
}

func TestLedger_Add_EmptyContent_Skipped(t *testing.T) {
	var l Ledger
	l.Add(HarnessSystem, "harness", "")
	if len(l.Entries) != 0 {
		t.Errorf("Add with empty content appended %d entries, want 0", len(l.Entries))
	}
}

func TestLedger_AddWithTokens_OverridesEstimate(t *testing.T) {
	var l Ledger
	l.AddWithTokens(HarnessSystem, "harness", "hhhh", 999)
	if len(l.Entries) != 1 {
		t.Fatalf("Entries len = %d, want 1", len(l.Entries))
	}
	if l.Entries[0].TokenEstimate != 999 {
		t.Errorf("TokenEstimate = %d, want 999 (Estimate would be %d)", l.Entries[0].TokenEstimate, Estimate("hhhh"))
	}
}

// TestLedger_AddMCPServer_AppendsByCallOrder pins the additive
// append behavior of AddMCPServer: two servers added in order
// appear in the slice in that order; the slice length is the number
// of additions; the per-server fields are reachable by index.
func TestLedger_AddMCPServer_AppendsByCallOrder(t *testing.T) {
	var l Ledger
	l.AddMCPServer(MCPServerInfo{Name: "weather", Transport: "http", Endpoint: "http://127.0.0.1:7777/mcp"})
	l.AddMCPServer(MCPServerInfo{Name: "local-stdio", Transport: "stdio", Command: []string{"local-mcp"}})

	if len(l.MCPServers) != 2 {
		t.Fatalf("MCPServers len = %d, want 2", len(l.MCPServers))
	}
	if l.MCPServers[0].Name != "weather" {
		t.Errorf("MCPServers[0].Name = %q, want %q", l.MCPServers[0].Name, "weather")
	}
	if l.MCPServers[1].Name != "local-stdio" {
		t.Errorf("MCPServers[1].Name = %q, want %q", l.MCPServers[1].Name, "local-stdio")
	}
	if l.MCPServers[0].Endpoint != "http://127.0.0.1:7777/mcp" {
		t.Errorf("MCPServers[0].Endpoint = %q, want http URL", l.MCPServers[0].Endpoint)
	}
	if len(l.MCPServers[1].Command) != 1 || l.MCPServers[1].Command[0] != "local-mcp" {
		t.Errorf("MCPServers[1].Command = %v, want [local-mcp]", l.MCPServers[1].Command)
	}
}

// TestLedger_MCPSummary_EmptyAndPopulated pins the two boundary
// forms of MCPSummary: zero servers declared → empty string (the
// cmd-side treats "" as "no MCP section to append"); at least one
// server declared → multi-line output that includes the server
// name, transport, endpoint/command, and the tool list.
func TestLedger_MCPSummary_EmptyAndPopulated(t *testing.T) {
	var l Ledger
	if got := l.MCPSummary(); got != "" {
		t.Errorf("empty Ledger MCPSummary() = %q, want \"\"", got)
	}

	l.AddMCPServer(MCPServerInfo{
		Name:            "weather",
		Transport:       "http",
		Endpoint:        "http://127.0.0.1:7777/mcp",
		Permission:      "read_only",
		FinalNames:      []string{"tool_alpha", "tool_beta"},
		OriginalCount:   2,
		RegisteredCount: 2,
	})
	got := l.MCPSummary()
	for _, want := range []string{
		"MCP servers (1 declared, 2 tools registered):",
		"weather",
		"transport=http",
		"permission=read_only",
		"http://127.0.0.1:7777/mcp",
		"tool_alpha",
		"tool_beta",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MCPSummary() missing substring %q (got=%q)", want, got)
		}
	}

	// stdio branch — the command shape must be present.
	var l2 Ledger
	l2.AddMCPServer(MCPServerInfo{
		Name:            "local-stdio",
		Transport:       "stdio",
		Command:         []string{"/usr/local/bin/local-mcp", "--quiet"},
		Permission:      "workspace_write",
		RegisteredCount: 0,
		OriginalCount:   1,
	})
	got2 := l2.MCPSummary()
	if !strings.Contains(got2, "stdio") {
		t.Errorf("stdio MCPSummary() missing transport marker (got=%q)", got2)
	}
	if !strings.Contains(got2, "/usr/local/bin/local-mcp --quiet") {
		t.Errorf("stdio MCPSummary() missing command shape (got=%q)", got2)
	}
}

// TestLedger_MCPDoctor_EmptyAndPopulated pins the three diagnostic
// branches of MCPDoctor: zero servers → nil; a single happy server
// (small + non-zero) → nil; a large server (> 20 registered tools)
// → "mcp_large_server" finding; a server with zero registered tools
// → "mcp_unreachable" finding (informational, not a failure).
func TestLedger_MCPDoctor_EmptyAndPopulated(t *testing.T) {
	if got := (&Ledger{}).MCPDoctor(); got != nil {
		t.Errorf("empty Ledger MCPDoctor() = %+v, want nil", got)
	}

	// Happy path: small + non-zero → no findings.
	var happy Ledger
	happy.AddMCPServer(MCPServerInfo{
		Name:            "weather",
		Transport:       "http",
		Endpoint:        "http://127.0.0.1:7777/mcp",
		RegisteredCount: 3,
		OriginalCount:   3,
	})
	if got := happy.MCPDoctor(); got != nil {
		t.Errorf("happy Ledger MCPDoctor() = %+v, want nil", got)
	}

	// Large server: 21 registered tools → mcp_large_server finding.
	var large Ledger
	large.AddMCPServer(MCPServerInfo{
		Name:            "monolith",
		Transport:       "stdio",
		RegisteredCount: 21,
		OriginalCount:   50,
	})
	findings := large.MCPDoctor()
	if len(findings) != 1 {
		t.Fatalf("large Ledger MCPDoctor() returned %d findings, want 1 (got=%+v)", len(findings), findings)
	}
	if findings[0].Type != "mcp_large_server" {
		t.Errorf("finding Type = %q, want %q", findings[0].Type, "mcp_large_server")
	}
	if findings[0].Name != "monolith" {
		t.Errorf("finding Name = %q, want %q", findings[0].Name, "monolith")
	}
	if !strings.Contains(findings[0].Detail, "21") {
		t.Errorf("finding Detail %q does not mention the 21-tool count", findings[0].Detail)
	}

	// Zero registered tools → mcp_unreachable finding
	// (informational, not a failure).
	var zero Ledger
	zero.AddMCPServer(MCPServerInfo{
		Name:            "allowlisted-out",
		Transport:       "http",
		Endpoint:        "http://127.0.0.1:7777/mcp",
		RegisteredCount: 0,
		OriginalCount:   4,
	})
	findings = zero.MCPDoctor()
	if len(findings) != 1 {
		t.Fatalf("zero-registered Ledger MCPDoctor() returned %d findings, want 1 (got=%+v)", len(findings), findings)
	}
	if findings[0].Type != "mcp_unreachable" {
		t.Errorf("finding Type = %q, want %q", findings[0].Type, "mcp_unreachable")
	}
	if findings[0].Name != "allowlisted-out" {
		t.Errorf("finding Name = %q, want %q", findings[0].Name, "allowlisted-out")
	}

	// Mixed: one large server + one zero-registered → two findings.
	var mixed Ledger
	mixed.AddMCPServer(MCPServerInfo{Name: "big", Transport: "http", RegisteredCount: 25})
	mixed.AddMCPServer(MCPServerInfo{Name: "empty", Transport: "http", RegisteredCount: 0})
	mixedFindings := mixed.MCPDoctor()
	if len(mixedFindings) != 2 {
		t.Errorf("mixed Ledger MCPDoctor() returned %d findings, want 2 (got=%+v)", len(mixedFindings), mixedFindings)
	}
}