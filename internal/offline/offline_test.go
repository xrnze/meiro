package offline

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/elianiva/meiro/youtube"
)

func song(id string, single bool) Song {
	return Song{Item: youtube.MusicItem{VideoID: id, Title: id}, File: id + ".webm", Single: single}
}

func playlist(id string, tracks ...string) Collection {
	return Collection{Item: youtube.MusicItem{BrowseID: id, Kind: youtube.KindPlaylist}, Tracks: tracks}
}

func TestMovePublishesOnlyACompleteLibrary(t *testing.T) {
	for _, scenario := range []string{"success", "bad source", "missing file", "occupied destination", "failed location save"} {
		t.Run(scenario, func(t *testing.T) {
			from, to := t.TempDir(), filepath.Join(t.TempDir(), "Meiro Downloads")
			track := song("move-me", true)
			track.File = fmt.Sprintf("%x.webm", sha256.Sum256([]byte(track.Item.VideoID)))
			put := func(path, data string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			put(filepath.Join(from, track.File), "track bytes")
			put(filepath.Join(from, "notes.txt"), "not a download")
			var lib Library
			lib.Add(track)
			lib.AddCollection(playlist("VLPLmove", "move-me"))
			switch scenario {
			case "bad source":
				lib.Add(Song{Item: youtube.MusicItem{VideoID: "outside"}, File: "../notes.txt"})
			case "missing file":
				missing := song("missing", true)
				missing.File = fmt.Sprintf("%x.webm", sha256.Sum256([]byte(missing.Item.VideoID)))
				lib.Add(missing)
			case "occupied destination":
				if err := os.Mkdir(to, 0o700); err != nil {
					t.Fatal(err)
				}
				put(filepath.Join(to, "notes.txt"), "already there")
			}
			if err := Write(from, lib); err != nil {
				t.Fatal(err)
			}
			published := false
			err := Move(t.Context(), from, to, lib, func() error {
				published = true
				if _, err := os.Stat(filepath.Join(from, track.File)); err != nil {
					t.Errorf("source disappeared before the location could be saved: %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(to, track.File)); err != nil || string(data) != "track bytes" {
					t.Errorf("destination did not contain the copied track: %q, %v", data, err)
				}
				got, err := Open(t.Context(), to)
				if err != nil {
					t.Error(err)
				}
				if collection, ok := got.Collection("VLPLmove"); !ok || !slices.Equal(collection.Tracks, []string{"move-me"}) {
					t.Errorf("destination did not contain the playlist: %+v", collection)
				}
				if scenario == "failed location save" {
					return errors.New("write refused")
				}
				return nil
			})
			if (err == nil) != (scenario == "success") || published != (scenario == "success" || scenario == "failed location save") {
				t.Errorf("move returned %v, published=%t", err, published)
			}
			if data, err := os.ReadFile(filepath.Join(from, "notes.txt")); err != nil || string(data) != "not a download" {
				t.Errorf("unrelated source file changed: %q, %v", data, err)
			}
			_, err = os.Stat(filepath.Join(from, track.File))
			if scenario == "success" && !errors.Is(err, os.ErrNotExist) || scenario != "success" && err != nil {
				t.Errorf("source track has incorrect state after move: %v", err)
			}
			switch scenario {
			case "occupied destination":
				if data, err := os.ReadFile(filepath.Join(to, "notes.txt")); err != nil || string(data) != "already there" {
					t.Errorf("occupied destination changed: %q, %v", data, err)
				}
			case "success":
			default:
				if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("failed move left a destination: %v", err)
				}
			}
		})
	}
}

func TestOpenRejectsAnIndexThatReferencesUnmanagedFiles(t *testing.T) {
	dir := t.TempDir()
	var lib Library
	lib.Add(Song{Item: youtube.MusicItem{VideoID: "one"}, File: "../notes.txt"})
	if err := Write(dir, lib); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), dir); err == nil {
		t.Error("opened a custom library that references a file outside it")
	}
}

// Removing a playlist removes the songs downloaded only for it, and keeps the
// ones downloaded by themselves or held by another downloaded playlist.
func TestRemovingACollectionKeepsSongsWantedElsewhere(t *testing.T) {
	var lib Library
	for _, s := range []Song{song("only", false), song("single", true), song("shared", false), song("other", false)} {
		lib.Add(s)
	}
	lib.AddCollection(playlist("VLPLa", "only", "single", "shared"))
	lib.AddCollection(playlist("VLPLb", "shared", "other"))

	files := lib.RemoveCollection("VLPLa")
	if !slices.Equal(files, []string{"only.webm"}) {
		t.Errorf("removed files = %q, want only the song downloaded for the playlist alone", files)
	}
	for _, id := range []string{"single", "shared", "other"} {
		if _, ok := lib.Song(id); !ok {
			t.Errorf("song %q should stay", id)
		}
	}
	if _, ok := lib.Collection("VLPLa"); ok {
		t.Error("the playlist should be gone")
	}
}

// The library survives a restart, and files a stopped download left behind,
// which it does not list, are deleted while everything else in the folder
// stays.
func TestReadKeepsListedSongsAndSweepsUnlistedAudio(t *testing.T) {
	dir := t.TempDir()
	listed := fmt.Sprintf("%x.webm", sha256.Sum256([]byte("v1")))
	unlisted := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.m4a"
	for _, name := range []string{listed, unlisted, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var lib Library
	lib.Add(Song{Item: youtube.MusicItem{VideoID: "v1", Title: "One"}, File: listed})
	lib.AddCollection(playlist("VLPLa", "v1"))
	if err := Write(dir, lib); err != nil {
		t.Fatal(err)
	}

	read, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := read.Song("v1"); !ok || s.File != listed || s.Item.Title != "One" {
		t.Fatalf("song after reading = %+v, %v", s, ok)
	}
	if c, ok := read.Collection("VLPLa"); !ok || !slices.Equal(c.Tracks, []string{"v1"}) {
		t.Fatalf("playlist after reading = %+v, %v", c, ok)
	}
	for name, want := range map[string]bool{listed: true, unlisted: false, "notes.txt": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}
