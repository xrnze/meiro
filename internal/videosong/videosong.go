// Package videosong remembers the song that plays in place of each music
// video, by video ID, between runs. When a chosen track is a music video,
// the song version of the same track plays instead, as it does for YouTube
// Music Premium members; the song found for a video is kept here, so that
// every video costs at most one lookup, ever.
package videosong

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"sync"

	"github.com/elianiva/meiro/internal/fsutil"
	"github.com/elianiva/meiro/youtube"
)

// MaxEntries bounds the cache; the oldest entries go first. An entry is a few
// dozen bytes, so this is well under a megabyte on disk.
const MaxEntries = 5000

type entry struct {
	ID string `json:"id"`
	youtube.MusicItem
}

// Cache is the songs known, safe for concurrent use. Changes are in memory
// at once and written by a goroutine of the cache's own. The zero value
// keeps them in memory only.
type Cache struct {
	path string

	mu      sync.Mutex
	known   map[string]youtube.MusicItem
	order   []string // oldest first
	dirty   bool
	running bool
	done    chan struct{}
}

// Open reads the cache kept at path. A file that is missing or unreadable is
// an empty cache.
func Open(path string) *Cache {
	c := &Cache{path: path, known: map[string]youtube.MusicItem{}}
	if path == "" {
		return c
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("videosong: reading %s: %v", path, err)
		}
		return c
	}
	var entries []entry
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("videosong: %s is not readable, starting fresh: %v", path, err)
		return c
	}
	for _, e := range entries {
		c.putLocked(e.ID, e.MusicItem)
	}
	return c
}

// Get is what is known of a video: the song that plays in its place, or the
// zero item when the video was found to have no song version.
func (c *Cache) Get(videoID string) (youtube.MusicItem, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	got, known := c.known[videoID]
	return got, known
}

// Put keeps the song that plays in place of a video. A song without a video
// ID records that the video has no song version, so it is not looked up
// again either.
func (c *Cache) Put(videoID string, song youtube.MusicItem) {
	if videoID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known == nil {
		c.known = map[string]youtube.MusicItem{}
	}
	// The zero item, the answer that a video has no song, must be kept too:
	// a missing entry reads back as the zero item as well.
	if have, ok := c.known[videoID]; ok && have == song {
		return
	}
	c.putLocked(videoID, song)
	c.changedLocked()
}

// Len is how many videos are known.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.known)
}

func (c *Cache) putLocked(videoID string, song youtube.MusicItem) {
	if _, ok := c.known[videoID]; ok {
		c.order = slices.DeleteFunc(c.order, func(have string) bool { return have == videoID })
	}
	c.known[videoID] = song
	c.order = append(c.order, videoID)
	for len(c.order) > MaxEntries {
		delete(c.known, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *Cache) changedLocked() {
	if c.path == "" {
		return
	}
	c.dirty = true
	if c.running {
		return
	}
	c.running, c.done = true, make(chan struct{})
	go c.drain(c.done)
}

func (c *Cache) drain(done chan struct{}) {
	defer close(done)
	for {
		c.mu.Lock()
		if !c.dirty {
			c.running = false
			c.mu.Unlock()
			return
		}
		entries := make([]entry, 0, len(c.order))
		for _, id := range c.order {
			entries = append(entries, entry{id, c.known[id]})
		}
		c.dirty = false
		c.mu.Unlock()
		data, err := json.Marshal(entries)
		if err == nil {
			err = fsutil.WriteFileAtomic(c.path, data, 0o600)
		}
		if err != nil {
			log.Printf("videosong: saving: %v", err)
		}
	}
}

// Wait returns once what changed so far is on disk.
func (c *Cache) Wait() {
	c.mu.Lock()
	done, running := c.done, c.running
	c.mu.Unlock()
	if running {
		<-done
	}
}
