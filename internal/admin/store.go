package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ErrBusy means another thefeed-admin has the database open.
var ErrBusy = errors.New("another thefeed-admin is running and holds the panel's database")

func dbPath(dir string) string { return filepath.Join(dir, "admin.db") }

func openDB(dir string) (*bolt.DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(dbPath(dir), 0o600, &bolt.Options{Timeout: time.Second})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath(dir), err)
	}
	return db, nil
}

// Bucket is one bbolt bucket. Get, Put and Delete store JSON under string
// keys; View and Update give the raw bucket for cursors, ranges and batches.
type Bucket struct {
	db   *bolt.DB
	name []byte
}

func newBucket(db *bolt.DB, name string) (*Bucket, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(name))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("bucket %s: %w", name, err)
	}
	return &Bucket{db: db, name: []byte(name)}, nil
}

// Get decodes the value at key into v and reports whether there was one.
func (b *Bucket) Get(key string, v any) (found bool, err error) {
	err = b.View(func(bk *bolt.Bucket) error {
		raw := bk.Get([]byte(key))
		if raw == nil {
			return nil
		}
		found = true
		return json.Unmarshal(raw, v)
	})
	return found, err
}

// Put stores v at key as JSON.
func (b *Bucket) Put(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Update(func(bk *bolt.Bucket) error { return bk.Put([]byte(key), raw) })
}

// Delete removes key; a missing key is fine.
func (b *Bucket) Delete(key string) error {
	return b.Update(func(bk *bolt.Bucket) error { return bk.Delete([]byte(key)) })
}

// View runs fn in a read transaction.
func (b *Bucket) View(fn func(*bolt.Bucket) error) error {
	return b.db.View(func(tx *bolt.Tx) error { return fn(tx.Bucket(b.name)) })
}

// Update runs fn in a write transaction.
func (b *Bucket) Update(fn func(*bolt.Bucket) error) error {
	return b.db.Update(func(tx *bolt.Tx) error { return fn(tx.Bucket(b.name)) })
}

// deleteWhere removes every key for which drop says so. Keys are gathered
// first: deleting under a live bbolt cursor can skip the next key.
func (b *Bucket) deleteWhere(drop func(key, raw []byte) bool) error {
	return b.Update(func(bk *bolt.Bucket) error {
		var gone [][]byte
		bk.ForEach(func(k, v []byte) error {
			if drop(k, v) {
				gone = append(gone, append([]byte(nil), k...))
			}
			return nil
		})
		for _, k := range gone {
			if err := bk.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
