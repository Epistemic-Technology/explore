package cache

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestExportImportRoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	src, err := Open(filepath.Join(srcDir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	// Populate two entries.
	hashA := HashSource([]byte("file A"))
	keyA := Key(hashA, "file", "claude-sonnet-4-6", PromptVersion)
	expA := &model.Explanation{Prose: "A prose", SourceHash: hashA, Model: "claude-sonnet-4-6", PromptVer: PromptVersion, CreatedAt: time.Now()}
	hashB := HashSource([]byte("symbol B"))
	keyB := Key(hashB, "symbol", "claude-sonnet-4-6", PromptVersion)
	expB := &model.Explanation{Prose: "B prose", SourceHash: hashB, Model: "claude-sonnet-4-6", PromptVer: PromptVersion, CreatedAt: time.Now()}
	if err := src.Put(keyA, expA); err != nil {
		t.Fatal(err)
	}
	if err := src.Put(keyB, expB); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := src.Export(&buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Sanity: human-readable; keys grep-able.
	if !strings.Contains(buf.String(), hashA) || !strings.Contains(buf.String(), "file") {
		t.Errorf("expected hash and level in export; got %s", buf.String())
	}

	// Import into a fresh cache; both entries should land.
	dstDir := t.TempDir()
	dst, err := Open(filepath.Join(dstDir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	res, err := dst.Import(bytes.NewReader(buf.Bytes()), false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Added != 2 || res.Skipped != 0 {
		t.Errorf("Import = %+v, want added=2 skipped=0", res)
	}
	gotA, _ := dst.Get(keyA)
	if gotA == nil || gotA.Prose != "A prose" {
		t.Errorf("A not imported correctly: %+v", gotA)
	}
}

func TestImportSkipsExistingByDefault(t *testing.T) {
	dstDir := t.TempDir()
	dst, err := Open(filepath.Join(dstDir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	hash := HashSource([]byte("shared"))
	key := Key(hash, "file", "claude-sonnet-4-6", PromptVersion)
	local := &model.Explanation{Prose: "LOCAL", SourceHash: hash, Model: "claude-sonnet-4-6", PromptVer: PromptVersion}
	if err := dst.Put(key, local); err != nil {
		t.Fatal(err)
	}

	// Synthesize an export with the same key but different prose.
	imported := exportFile{
		FormatVersion: ExportFormatVersion,
		Entries: []exportEntry{
			{Key: string(key), Explanation: &model.Explanation{Prose: "IMPORTED", SourceHash: hash, Model: "claude-sonnet-4-6", PromptVer: PromptVersion}},
		},
	}
	var buf bytes.Buffer
	if err := encodeForTest(&buf, imported); err != nil {
		t.Fatal(err)
	}

	res, err := dst.Import(&buf, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Added != 0 || res.Skipped != 1 {
		t.Errorf("Import = %+v, want added=0 skipped=1", res)
	}
	got, _ := dst.Get(key)
	if got.Prose != "LOCAL" {
		t.Errorf("expected local prose preserved; got %q", got.Prose)
	}

	// Now with overwrite=true.
	buf.Reset()
	if err := encodeForTest(&buf, imported); err != nil {
		t.Fatal(err)
	}
	res, err = dst.Import(&buf, true)
	if err != nil {
		t.Fatalf("Import overwrite: %v", err)
	}
	if res.Added != 1 {
		t.Errorf("overwrite Import = %+v, want added=1", res)
	}
	got, _ = dst.Get(key)
	if got.Prose != "IMPORTED" {
		t.Errorf("expected overwrite; got %q", got.Prose)
	}
}

func TestImportRejectsBadVersion(t *testing.T) {
	dst, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	bad := strings.NewReader(`{"format_version": 999, "entries": []}`)
	_, err = dst.Import(bad, false)
	if err == nil || !strings.Contains(err.Error(), "format version") {
		t.Errorf("expected version error; got %v", err)
	}
}

// encodeForTest writes an exportFile to w as the same format Export produces.
func encodeForTest(w *bytes.Buffer, f exportFile) error {
	return json.NewEncoder(w).Encode(f)
}
