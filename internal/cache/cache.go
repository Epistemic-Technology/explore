package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
