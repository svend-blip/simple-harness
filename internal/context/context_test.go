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