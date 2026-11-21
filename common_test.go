package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestResolveSelection(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		limit      int
		wantIDs    []int
		wantReason string
	}{
		{
			name:    "single IDs and spaces",
			input:   "1 3 5",
			limit:   5,
			wantIDs: []int{1, 3, 5},
		},
		{
			name:    "ranges with hyphen, double-dot, and single-dot",
			input:   "1-2 4..5 7.8",
			limit:   10,
			wantIDs: []int{1, 2, 4, 5, 7, 8},
		},
		{
			name:    "reversed range is normalized",
			input:   "4-2",
			limit:   5,
			wantIDs: []int{2, 3, 4},
		},
		{
			name:    "exclusions with caret",
			input:   "1-5 ^2 ^4-5",
			limit:   5,
			wantIDs: []int{1, 3},
		},
		{
			name:    "duplicates are deduplicated and sorted",
			input:   "3 1-3 2",
			limit:   5,
			wantIDs: []int{1, 2, 3},
		},
		{
			name:    "all excluded returns empty slice without error",
			input:   "1-2 ^1-2",
			limit:   3,
			wantIDs: nil,
		},
		{
			name:       "out of range IDs reported together",
			input:      "0 2 6 8 ^7",
			limit:      5,
			wantReason: "ID(s) 0, 6, 7, 8 are out of range (1-5).",
		},
		{
			name:       "invalid token rejected",
			input:      "1-3 foo",
			limit:      5,
			wantReason: "'foo' is not a valid ID or range.",
		},
		{
			name:       "excessive range rejected",
			input:      "1-2000000",
			limit:      5,
			wantReason: "'1-2000000' is not a valid ID or range.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotIDs, gotReason := resolveSelection(tc.input, tc.limit)
			if gotReason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
			if !reflect.DeepEqual(gotIDs, tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	raw := strings.Join([]string{
		" M add.go",
		"A  new.go",
		"R  new_name.go",
		"old_name.go",
		"?? untracked.txt",
		"MM both.go",
		"",
	}, "\x00")

	got := parseStatus(raw)
	want := []entry{
		{code: " M", path: "add.go"},
		{code: "A ", path: "new.go"},
		{code: "R ", path: "new_name.go"},
		{code: "??", path: "untracked.txt"},
		{code: "MM", path: "both.go"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStatus() = %+v, want %+v", got, want)
	}
}

func TestDisplayCommand(t *testing.T) {
	got := displayCommand([]string{"stash", "push", "-m", "my stash's msg"})
	want := "git stash push -m 'my stash''s msg'"
	if got != want {
		t.Fatalf("displayCommand() = %q, want %q", got, want)
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string][2]string{
		"M ": {"Staged", "Green"},
		" M": {"Modified", "Yellow"},
		"MM": {"Modified*", "Yellow"},
		"??": {"Untracked", "Green"},
		" D": {"Deleted", "Red"},
		"UU": {"CONFLICT", "Magenta"},
		"!!": {"Ignored", "Gray"},
		"XY": {"Changed", "Gray"},
	}
	for code, want := range cases {
		label, color := describe(code)
		if label != want[0] || color != want[1] {
			t.Errorf("describe(%q) = (%q, %q), want (%q, %q)", code, label, color, want[0], want[1])
		}
	}
}

func TestUsageOutput(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	out := buf.String()
	for _, want := range []string{"usage: ezgit <command>", "status", "add"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage() missing %q in:\n%s", want, out)
		}
	}
}