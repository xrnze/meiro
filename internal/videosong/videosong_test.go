package videosong

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/elianiva/meiro/youtube"
)

// TestCacheRemembersSongsAcrossOpens checks that a video is looked up once,
// ever: the song found for it, and the finding that one has no song, are
// both read back from disk by a new cache.
func TestCacheRemembersSongsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video-songs.json")
	c := Open(path)
	c.Put("video-1", youtube.MusicItem{VideoID: "song-1", Title: "Searched Song"})
	c.Put("video-2", youtube.MusicItem{}) // found to have no song
	c.Wait()

	c = Open(path)
	if song, ok := c.Get("video-1"); !ok || song.VideoID != "song-1" {
		t.Errorf("video-1 came back %q (known %v), want song-1", song.VideoID, ok)
	}
	if _, ok := c.Get("video-2"); !ok {
		t.Errorf("video-2's no-song answer did not survive an open")
	}
	if _, ok := c.Get("video-3"); ok {
		t.Errorf("an unknown video was answered from the cache")
	}
}

// TestCacheBounds checks that the oldest entries are forgotten first, so the
// cache cannot grow without bound.
func TestCacheBounds(t *testing.T) {
	c := Open("")
	for i := range MaxEntries + 10 {
		c.Put(fmt.Sprintf("video-%d", i), youtube.MusicItem{VideoID: "song"})
	}
	if got := c.Len(); got != MaxEntries {
		t.Fatalf("cache holds %d entries, want %d", got, MaxEntries)
	}
	if _, ok := c.Get("video-0"); ok {
		t.Errorf("the oldest entry stayed past the bound")
	}
	if _, ok := c.Get(fmt.Sprintf("video-%d", MaxEntries+9)); !ok {
		t.Errorf("the newest entry was forgotten")
	}
}

// TestACacheOfANewerMatcherStartsFresh reads a file a newer build wrote:
// this build did not judge its answers, so it trusts none of them.
func TestACacheOfANewerMatcherStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video-songs.json")
	data, err := json.Marshal(file{
		Version: fileVersion + 1,
		Entries: []entry{
			{"video-1", youtube.MusicItem{VideoID: "song-1", Title: "Searched Song"}},
			{"video-2", youtube.MusicItem{}}, // judged songless by a newer matcher
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := Open(path)
	if _, ok := c.Get("video-1"); ok {
		t.Errorf("a newer matcher's song pick was trusted")
	}
	if _, ok := c.Get("video-2"); ok {
		t.Errorf("a newer matcher's no-song answer was trusted")
	}
}

// TestCacheOfTheLastMatcherStartsFresh reads the format the build before
// this one left on disk, a versioned file one version short: it holds the
// answers most users have, and none of them are trusted.
func TestCacheOfTheLastMatcherStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video-songs.json")
	data, err := json.Marshal(file{
		Version: fileVersion - 1,
		Entries: []entry{
			{"video-1", youtube.MusicItem{VideoID: "song-1", Title: "Searched Song"}},
			{"video-2", youtube.MusicItem{}}, // judged songless by the last matcher
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := Open(path)
	if _, ok := c.Get("video-1"); ok {
		t.Errorf("the last matcher's song pick was trusted")
	}
	if _, ok := c.Get("video-2"); ok {
		t.Errorf("the last matcher's no-song answer was trusted")
	}
}

// TestCacheOfAnotherMatcherStartsFresh reads a file a matcher of another
// version wrote: its picks may not be the ones this matcher would make, so
// nothing in it is trusted and every video is asked for again.
func TestCacheOfAnotherMatcherStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video-songs.json")
	legacy := []entry{
		{"video-1", youtube.MusicItem{VideoID: "song-1", Title: "Searched Song"}},
		{"video-2", youtube.MusicItem{}}, // judged songless by the other matcher
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := Open(path)
	if _, ok := c.Get("video-1"); ok {
		t.Errorf("the other matcher's song pick was trusted")
	}
	if _, ok := c.Get("video-2"); ok {
		t.Errorf("the other matcher's no-song answer was trusted")
	}
}

// TestCacheKeepingAnAnswer records the no-song answer, which is the zero
// item, alongside the song answers.
func TestCacheKeepsTheNoSongAnswer(t *testing.T) {
	c := Open("")
	c.Put("video-1", youtube.MusicItem{})
	if song, ok := c.Get("video-1"); !ok || song.VideoID != "" {
		t.Errorf("the no-song answer came back %q (known %v), want the zero item", song.VideoID, ok)
	}
}
