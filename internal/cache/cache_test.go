package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mikethicke/explore/internal/model"
)

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hash := HashSource([]byte("hello"))
	key := Key(hash, "file", "claude-sonnet-4-6", PromptVersion)
	exp := &model.Explanation{
		Prose:      "An example.",
		SourceHash: hash,
		Model:      "claude-sonnet-4-6",
		PromptVer:  PromptVersion,
		CreatedAt:  time.Now(),
	}
	if err := c.Put(key, exp); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Prose != "An example." {
		t.Fatalf("unexpected: %+v", got)
	}
	// Miss on different key.
	miss, err := c.Get(Key("other", "file", "x", 1))
	if err != nil {
		t.Fatal(err)
	}
	if miss != nil {
		t.Fatalf("expected miss, got %+v", miss)
	}
}
