package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/mikethicke/explore/internal/model"
)

const PromptVersion = 1

var bucket = []byte("explanations")

type Cache struct {
	db *bolt.DB
}

func Open(path string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o644, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(bucket)
		return e
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error { return c.db.Close() }

func HashSource(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func Key(sourceHash, level, model string, promptVer int) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%s", sourceHash, level, promptVer, model))
}

func (c *Cache) Get(key []byte) (*model.Explanation, error) {
	var out *model.Explanation
	err := c.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucket).Get(key)
		if v == nil {
			return nil
		}
		var e model.Explanation
		if err := json.Unmarshal(v, &e); err != nil {
			return err
		}
		out = &e
		return nil
	})
	return out, err
}

func (c *Cache) Put(key []byte, e *model.Explanation) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put(key, data)
	})
}

// ExportFormatVersion identifies the on-disk shape of exported JSON; bump if
// the entry layout changes so importers can refuse incompatible files.
const ExportFormatVersion = 1

type exportFile struct {
	FormatVersion int            `json:"format_version"`
	Entries       []exportEntry  `json:"entries"`
}

type exportEntry struct {
	// Key is the stringy composite produced by Key() — kept as-is so the file
	// is grep-able and humans can spot which model / prompt-version an entry
	// belongs to.
	Key         string             `json:"key"`
	Explanation *model.Explanation `json:"explanation"`
}

// Export writes every cache entry to w as a single JSON document. The output
// is portable across machines because the cache key already encodes
// source-hash + level + prompt-version + model — no host paths are involved.
func (c *Cache) Export(w io.Writer) error {
	out := exportFile{FormatVersion: ExportFormatVersion}
	err := c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		return b.ForEach(func(k, v []byte) error {
			var e model.Explanation
			if err := json.Unmarshal(v, &e); err != nil {
				// Skip rather than fail the whole export — a single bad row
				// shouldn't block sharing the rest.
				return nil
			}
			out.Entries = append(out.Entries, exportEntry{
				Key:         string(k),
				Explanation: &e,
			})
			return nil
		})
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// ImportResult reports what changed during an import.
type ImportResult struct {
	Added   int
	Skipped int // existing key, overwrite was false
}

// Import reads a JSON export written by Export and merges entries into this
// cache. When overwrite is false, entries whose key already exists locally are
// left untouched. Returns an error if the file isn't a recognized export or
// the format version doesn't match.
func (c *Cache) Import(r io.Reader, overwrite bool) (ImportResult, error) {
	var in exportFile
	dec := json.NewDecoder(r)
	if err := dec.Decode(&in); err != nil {
		return ImportResult{}, fmt.Errorf("decode export: %w", err)
	}
	if in.FormatVersion != ExportFormatVersion {
		return ImportResult{}, fmt.Errorf("unsupported export format version %d (want %d)", in.FormatVersion, ExportFormatVersion)
	}
	var res ImportResult
	err := c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)
		for _, e := range in.Entries {
			if e.Explanation == nil {
				continue
			}
			key := []byte(e.Key)
			if !overwrite && b.Get(key) != nil {
				res.Skipped++
				continue
			}
			data, err := json.Marshal(e.Explanation)
			if err != nil {
				return err
			}
			if err := b.Put(key, data); err != nil {
				return err
			}
			res.Added++
		}
		return nil
	})
	return res, err
}
