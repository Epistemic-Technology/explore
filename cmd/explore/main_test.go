package main

import (
	"flag"
	"reflect"
	"testing"
)

// newTestFlagSet builds a flag set mirroring the real top-level flags so the
// bool-vs-value detection logic in reorderArgs exercises a realistic mix.
func newTestFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("cache-dir", "", "")
	fs.String("provider", "", "")
	fs.Bool("debug", false, "")
	fs.Bool("no-lsp", false, "")
	fs.Int("token-budget", -1, "")
	return fs
}

func TestReorderArgs_FlagAfterPositional(t *testing.T) {
	fs := newTestFlagSet()
	got := reorderArgs([]string{"/repo/path", "--debug"}, fs)
	want := []string{"--debug", "/repo/path"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_ValueFlagAfterPositional(t *testing.T) {
	fs := newTestFlagSet()
	got := reorderArgs([]string{"/repo", "--cache-dir", "/tmp"}, fs)
	want := []string{"--cache-dir", "/tmp", "/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_EqualsValueStaysIntact(t *testing.T) {
	fs := newTestFlagSet()
	got := reorderArgs([]string{"/repo", "--cache-dir=/tmp"}, fs)
	want := []string{"--cache-dir=/tmp", "/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_MixedFlagsAndPositionals(t *testing.T) {
	fs := newTestFlagSet()
	got := reorderArgs([]string{"--provider", "claude", "/repo", "--debug", "--token-budget", "1000"}, fs)
	want := []string{"--provider", "claude", "--debug", "--token-budget", "1000", "/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_DoubleDashStopsParsing(t *testing.T) {
	fs := newTestFlagSet()
	// Anything after `--` is positional, even if it looks like a flag.
	got := reorderArgs([]string{"--debug", "--", "--not-a-flag", "/repo"}, fs)
	want := []string{"--debug", "--not-a-flag", "/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_UnknownFlagTreatedAsBool(t *testing.T) {
	fs := newTestFlagSet()
	// We don't know if --weird takes a value; treat it as bool so we don't
	// accidentally swallow /repo. fs.Parse will then error on it loudly.
	got := reorderArgs([]string{"/repo", "--weird"}, fs)
	want := []string{"--weird", "/repo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReorderArgs_NoArgs(t *testing.T) {
	fs := newTestFlagSet()
	got := reorderArgs(nil, fs)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// End-to-end: the user's actual scenario, reordered through fs.Parse, should
// produce the expected flag values + positional arg.
func TestReorderArgs_IntegratesWithFlagParse(t *testing.T) {
	fs := newTestFlagSet()
	debugFlag := fs.Bool("debug2", false, "")
	cacheDir := fs.String("cache-dir2", "", "")
	args := reorderArgs([]string{"/some/repo", "--debug2", "--cache-dir2", "/tmp"}, fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}
	if !*debugFlag {
		t.Errorf("--debug2 should be true after reorder")
	}
	if *cacheDir != "/tmp" {
		t.Errorf("--cache-dir2 = %q, want /tmp", *cacheDir)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "/some/repo" {
		t.Errorf("positional NArg=%d Arg(0)=%q", fs.NArg(), fs.Arg(0))
	}
}
