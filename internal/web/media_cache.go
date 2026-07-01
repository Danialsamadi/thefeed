package web

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mediaCacheFileExt    = ".cache"
	mediaCacheMaxMime    = 200
	mediaCacheMaxFileExt = 1 << 26 // 64 MiB hard cap per cached file
)

// mediaDiskCache stores downloaded media blobs on disk so multiple devices
// connected to the same client/server share the cost of one DNS-tunnelled
// fetch. Entries are content-addressed by (size, crc32) and reaped after
// ttl based on file mtime.
//
// File format: each entry is a single file
//
//	<size>_<crc8hex>.cache
//
// containing:
//
//	2 bytes BE  — mime length
//	N bytes     — mime utf8
//	rest        — raw file bytes
type mediaDiskCache struct {
	dir string
	ttl time.Duration
	mu  sync.Mutex

	// crypto, when non-nil and unlocked, seals each entry's framed bytes
	// (header‖mime‖body) at rest with AES-256-GCM. Set only on the savedMedia
	// store; the ephemeral mediaCache leaves it nil (plaintext).
	crypto *savedCrypto

	// maxBytes is the byte budget (0 = unlimited). When exceeded after a Put,
	// the oldest entries are evicted until total <= 90% of maxBytes.
	maxBytes int64
	// curBytes is the running total of file sizes on disk. -1 means unknown
	// (scan needed). Updated on Put/Remove/Cleanup/Clear.
	curBytes int64
}

// setCrypto swaps the at-rest crypto handle under c.mu so it doesn't race the
// Get/Put readers (which snapshot it under the same lock).
func (c *mediaDiskCache) setCrypto(sc *savedCrypto) {
	c.mu.Lock()
	c.crypto = sc
	c.mu.Unlock()
}

// cryptoSnapshot returns the current crypto handle under c.mu. Callers use the
// returned pointer for the whole operation so a concurrent setCrypto can't tear
// the read.
func (c *mediaDiskCache) cryptoSnapshot() *savedCrypto {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.crypto
}

// sealActiveFor reports whether at-rest sealing should be applied for the given
// (snapshotted) crypto handle.
func sealActiveFor(sc *savedCrypto) bool {
	return sc != nil && !sc.locked
}

func newMediaDiskCache(dir string, ttl time.Duration) (*mediaDiskCache, error) {
	if dir == "" {
		return nil, errors.New("media cache dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &mediaDiskCache{dir: dir, ttl: ttl, curBytes: -1}, nil
}

func (c *mediaDiskCache) keyFile(size int64, crc uint32) string {
	return filepath.Join(c.dir, fmt.Sprintf("%d_%08x%s", size, crc, mediaCacheFileExt))
}

// SetMaxBytes sets the byte budget and enforces it immediately.
// 0 means unlimited.
func (c *mediaDiskCache) SetMaxBytes(n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxBytes = n
	if n > 0 {
		c.enforceBudgetLocked()
	}
}

// Size returns the total bytes on disk. Scans once if unknown.
func (c *mediaDiskCache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.curBytes < 0 {
		c.scanSizeLocked()
	}
	return c.curBytes
}

// resetSize zeroes the cached byte total (e.g. after an external wipe).
func (c *mediaDiskCache) resetSize() {
	c.mu.Lock()
	c.curBytes = 0
	c.mu.Unlock()
}

// scanSizeLocked computes curBytes from the directory listing.
func (c *mediaDiskCache) scanSizeLocked() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		c.curBytes = 0
		return
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), mediaCacheFileExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	c.curBytes = total
}

// enforceBudgetLocked evicts oldest entries until total <= 90% of maxBytes.
func (c *mediaDiskCache) enforceBudgetLocked() {
	if c.maxBytes <= 0 {
		return
	}
	if c.curBytes < 0 {
		c.scanSizeLocked()
	}
	if c.curBytes <= c.maxBytes {
		return
	}
	// 90% low-water mark (hysteresis)
	target := c.maxBytes * 9 / 10

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type fileEntry struct {
		name  string
		size  int64
		mtime time.Time
	}
	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), mediaCacheFileExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), size: info.Size(), mtime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if c.curBytes <= target {
			break
		}
		if os.Remove(filepath.Join(c.dir, f.name)) == nil {
			c.curBytes -= f.size
		}
	}
}

// Get returns the cached body and mime type if present and not expired.
// Touching mtime on hit so the entry stays alive while it's in use.
func (c *mediaDiskCache) Get(size int64, crc uint32) (body []byte, mime string, ok bool) {
	if size <= 0 || crc == 0 {
		return nil, "", false
	}
	path := c.keyFile(size, crc)
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", false
	}
	if c.ttl > 0 && time.Since(info.ModTime()) > c.ttl {
		if os.Remove(path) == nil {
			c.mu.Lock()
			if c.curBytes >= info.Size() {
				c.curBytes -= info.Size()
			}
			c.mu.Unlock()
		}
		return nil, "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false
	}
	sc := c.cryptoSnapshot()
	if sealActiveFor(sc) {
		if opened, oerr := openBytes(sc.dek, data); oerr == nil {
			data = opened
		} else {
			// Migration path: a blob written by the pre-encryption feature is a
			// plaintext frame. Try parsing it as-is; if valid, serve it and
			// re-seal in place so the next read takes the fast path.
			if b, m, okFrame := parseMediaFrame(data, size); okFrame {
				_ = c.Put(size, crc, b, m) // re-seal
				return b, m, true
			}
			return nil, "", false
		}
	}
	b, m, okFrame := parseMediaFrame(data, size)
	if !okFrame {
		return nil, "", false
	}
	_ = os.Chtimes(path, time.Now(), time.Now())
	return b, m, true
}

// parseMediaFrame decodes a header(2 BE mimeLen)‖mime‖body frame, validating
// that the body length matches the content-addressed size.
func parseMediaFrame(data []byte, size int64) (body []byte, mime string, ok bool) {
	if len(data) < 2 {
		return nil, "", false
	}
	mimeLen := int(binary.BigEndian.Uint16(data[:2]))
	if mimeLen > mediaCacheMaxMime || 2+mimeLen > len(data) {
		return nil, "", false
	}
	mime = string(data[2 : 2+mimeLen])
	body = data[2+mimeLen:]
	if int64(len(body)) != size {
		return nil, "", false // corrupt or partial write
	}
	return body, mime, true
}

// Put writes the body+mime atomically to the cache.
func (c *mediaDiskCache) Put(size int64, crc uint32, body []byte, mime string) error {
	if size <= 0 || crc == 0 || int64(len(body)) != size {
		return errors.New("media cache: invalid put")
	}
	// Snapshot the crypto handle once for the whole operation.
	sc := c.cryptoSnapshot()
	// Never write a plaintext blob into a sealed store that's locked — it would
	// both leak and desync the keyring.
	if sc != nil && sc.locked {
		return errSavedLocked
	}
	if len(body) > mediaCacheMaxFileExt {
		return errors.New("media cache: body too large")
	}
	if len(mime) > mediaCacheMaxMime {
		mime = mime[:mediaCacheMaxMime]
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Frame as header(2 BE mimeLen)‖mime‖body, then seal the whole frame at
	// rest if a crypto store is attached.
	framed := make([]byte, 2+len(mime)+len(body))
	binary.BigEndian.PutUint16(framed[:2], uint16(len(mime)))
	copy(framed[2:], mime)
	copy(framed[2+len(mime):], body)
	if sealActiveFor(sc) {
		framed = sealBytes(sc.dek, framed)
	}

	path := c.keyFile(size, crc)
	newSize := int64(len(framed))

	// Track the old file size for the delta.
	var oldSize int64
	if info, err := os.Stat(path); err == nil {
		oldSize = info.Size()
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, framed, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if c.curBytes >= 0 {
		c.curBytes += newSize - oldSize
	}
	c.enforceBudgetLocked()
	return nil
}

// Remove deletes the cache entry for (size, crc) if it exists.
func (c *mediaDiskCache) Remove(size int64, crc uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := c.keyFile(size, crc)
	if info, err := os.Stat(path); err == nil {
		if os.Remove(path) == nil && c.curBytes >= info.Size() {
			c.curBytes -= info.Size()
		}
	}
}

// Cleanup removes entries older than ttl. Returns the count removed.
func (c *mediaDiskCache) Cleanup() int {
	if c.ttl <= 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	now := time.Now()
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), mediaCacheFileExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > c.ttl {
			if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
				removed++
				if c.curBytes >= info.Size() {
					c.curBytes -= info.Size()
				}
			}
		}
	}
	return removed
}

// Clear deletes every cached entry. Returns the count removed.
func (c *mediaDiskCache) Clear() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), mediaCacheFileExt) {
			continue
		}
		if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
			removed++
		}
	}
	c.curBytes = 0
	return removed
}
