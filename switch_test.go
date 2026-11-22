package main

import (
	"strings"
	"testing"
	"time"
)

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		name, filter string
		want         bool
	}{
		{"feature/taxi-fare", "", true},
		{"feature/taxi-fare", "taxi", true},
		{"feature/taxi-fare", "FARE", true},
		{"feature/taxi-fare", "feature/*", true},
		{"feature/taxi-fare", "release", false},
	}
	for _, tt := range tests {
		if got := matchesFilter(tt.name, tt.filter); got != tt.want {
			t.Errorf("matchesFilter(%q, %q) = %v, want %v", tt.name, tt.filter, got, tt.want)
		}
	}
}

func TestArrangeAndFirstWithDate(t *testing.T) {
	older := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	branches := []branchRef{
		{name: "older", date: older, kind: "Local"},
		{name: "newer", date: newer, kind: "Local"},
	}

	arranged, latest := arrange(branches, false, 1)
	if len(arranged) != 1 || arranged[0].name != "newer" {
		t.Fatalf("arrange limit = %#v, want newest branch only", arranged)
	}
	if !latest.Equal(newer) {
		t.Fatalf("latest = %v, want %v", latest, newer)
	}
	if got := firstWithDate(arranged, latest); got != 0 {
		t.Fatalf("firstWithDate() = %d, want 0", got)
	}

	all, latest := arrange(branches, true, 1)
	if len(all) != 2 || all[0].name != "newer" {
		t.Fatalf("arrange searching = %#v, want newest-first", all)
	}
	if !latest.Equal(newer) {
		t.Fatalf("latest = %v, want %v", latest, newer)
	}
}

func TestStripRemoteAndQuoteName(t *testing.T) {
	if got := stripRemote("Origin/feature/x", "origin"); got != "feature/x" {
		t.Errorf("stripRemote() = %q, want %q", got, "feature/x")
	}
	if got := quoteName("feature/a'b"); got != "'feature/a''b'" {
		t.Errorf("quoteName() = %q", got)
	}
}

func TestRunSwitch_SwitchesToLocalBranch(t *testing.T) {
	dir := initTempRepo(t)

	if _, ok := git(dir, "switch", "-c", "feature/test"); !ok {
		t.Fatal("git switch -c feature/test failed")
	}
	writeRepoFile(t, dir, "feature.txt", "feature\n")
	if _, ok := git(dir, "add", "feature.txt"); !ok {
		t.Fatal("git add failed")
	}
	if _, ok := git(dir, "commit", "-m", "feature commit"); !ok {
		t.Fatal("git commit failed")
	}
	if _, ok := git(dir, "switch", "main"); !ok {
		t.Fatal("git switch main failed")
	}

	setMockStdin(t, "1\n")
	var code int
	out := captureOutput(t, func() {
		code = runSwitch([]string{"--no-tip"})
	})

	if code != 0 {
		t.Fatalf("runSwitch() = %d, want 0\n%s", code, out)
	}
	branch, ok := git(dir, "branch", "--show-current")
	if !ok || strings.TrimSpace(branch) != "feature/test" {
		t.Fatalf("current branch = %q, want feature/test", strings.TrimSpace(branch))
	}
	if !strings.Contains(out, "Switched to branch 'feature/test'") {
		t.Errorf("missing switched message in output:\n%s", out)
	}
}

func TestRunSwitch_InvalidBranchID(t *testing.T) {
	initTempRepo(t)
	setMockStdin(t, "999\n")

	var code int
	out := captureOutput(t, func() {
		code = runSwitch([]string{"--no-tip"})
	})

	if code != 1 {
		t.Fatalf("runSwitch() = %d, want 1", code)
	}
	if !strings.Contains(out, "Invalid branch ID") {
		t.Errorf("missing invalid branch ID error in output:\n%s", out)
	}
}
