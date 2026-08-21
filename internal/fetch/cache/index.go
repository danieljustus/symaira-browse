package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const indexFileName = "cache-index.json"

type indexEntry struct {
	Key      string    `json:"key"`
	Size     int64     `json:"size"`
	StoredAt time.Time `json:"stored_at"`
}

type cacheIndex struct {
	TotalSize int64        `json:"total_size"`
	Entries   []indexEntry `json:"entries"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type indexManager struct {
	mu     sync.RWMutex
	dir    string
	index  cacheIndex
	loaded bool
	dirty  bool
}

func newIndexManager(dir string) *indexManager {
	return &indexManager{
		dir: dir,
		index: cacheIndex{
			Entries: make([]indexEntry, 0),
		},
	}
}

func (im *indexManager) indexPath() string {
	return filepath.Join(im.dir, indexFileName)
}

// load reads the persisted index from disk. It returns valid=true only when
// an index file existed and parsed successfully — callers use this to skip
// a full cache-directory rescan when the on-disk index can be trusted as-is.
func (im *indexManager) load() (valid bool, err error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	if im.loaded {
		return false, nil
	}

	data, err := os.ReadFile(im.indexPath())
	if err != nil {
		im.loaded = true
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	var idx cacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		im.loaded = true
		return false, nil
	}

	im.index = idx
	im.loaded = true
	return true, nil
}

func (im *indexManager) save() error {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.index.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(im.index, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := im.indexPath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, im.indexPath()); err != nil {
		return err
	}

	im.dirty = false
	return nil
}

// addEntry inserts or updates an index entry. If the key already exists
// the prior size is subtracted before the new entry is stored, preventing
// duplicate accounting on overwrite.
func (im *indexManager) addEntry(key string, size int64, storedAt time.Time) {
	im.mu.Lock()
	defer im.mu.Unlock()

	for i, entry := range im.index.Entries {
		if entry.Key == key {
			im.index.TotalSize -= entry.Size
			im.index.Entries[i] = indexEntry{
				Key:      key,
				Size:     size,
				StoredAt: storedAt,
			}
			im.index.TotalSize += size
			im.dirty = true
			return
		}
	}

	im.index.Entries = append(im.index.Entries, indexEntry{
		Key:      key,
		Size:     size,
		StoredAt: storedAt,
	})
	im.index.TotalSize += size
	im.dirty = true
}

// rebuild replaces the entire index atomically. Used at startup to
// reconcile the index with the actual files on disk.
func (im *indexManager) rebuild(entries []indexEntry, totalSize int64) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.index.Entries = entries
	im.index.TotalSize = totalSize
	im.dirty = true
}

func (im *indexManager) removeEntry(key string) bool {
	im.mu.Lock()
	defer im.mu.Unlock()

	for i, entry := range im.index.Entries {
		if entry.Key == key {
			im.index.TotalSize -= entry.Size
			im.index.Entries = append(im.index.Entries[:i], im.index.Entries[i+1:]...)
			im.dirty = true
			return true
		}
	}
	return false
}

func (im *indexManager) getTotalSize() int64 {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.index.TotalSize
}

func (im *indexManager) getEntries() []indexEntry {
	im.mu.RLock()
	defer im.mu.RUnlock()
	entries := make([]indexEntry, len(im.index.Entries))
	copy(entries, im.index.Entries)
	return entries
}

func (im *indexManager) needsSave() bool {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.dirty
}
