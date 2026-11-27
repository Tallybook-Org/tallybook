package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// discardedWrite matches a write whose error is assigned to the blank
// identifier, whatever the writer is named. CLAUDE.md §8 forbids discarding
// errors, even in a handler, so every such write must check its error and
// log it instead.
var discardedWrite = regexp.MustCompile(`_, _ = \w+\.Write\(`)

// TestNoDiscardedResponseWrites guards every binary under cmd/ at once. The
// health and readiness handlers are closures inside run() and need a live
// database to exercise, so a behavioural test cannot reach them; scanning
// the source is the only check that covers all seven sites.
func TestNoDiscardedResponseWrites(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "*", "*.go"))
	if err != nil {
		t.Fatalf("glob cmd sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("glob cmd sources: found no files, scan would pass vacuously")
	}

	var offenders []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if discardedWrite.MatchString(line) {
				offenders = append(offenders, filepath.ToSlash(file)+":"+strconv.Itoa(i+1))
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("discarded response write errors found, want none: %s", strings.Join(offenders, ", "))
	}
}
