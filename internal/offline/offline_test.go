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
	for _, scenario := range []string{"success", "bad source", "missing file", "occupied destination", "failed location save", "durability unconfirmed"} {
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
			if err := (Folder{Dir: from}).Write(t.Context(), lib); err != nil {
				t.Fatal(err)
			}
			published := false
			target := Folder{Dir: to, ID: "test-destination"}
			var retainedDefault string
			kept, err := Move(t.Context(), Folder{Dir: from}, target, lib, "", func(digest string) (bool, error) {
				published = true
				retainedDefault = digest
				if _, err := os.Stat(filepath.Join(from, track.File)); err != nil {
					t.Errorf("source disappeared before the location could be saved: %v", err)
				}
				if data, err := os.ReadFile(filepath.Join(to, track.File)); err != nil || string(data) != "track bytes" {
					t.Errorf("destination did not contain the copied track: %q, %v", data, err)
				}
				got, err := target.Open(t.Context())
				if err != nil {
					t.Error(err)
				}
				if collection, ok := got.Collection("VLPLmove"); !ok || !slices.Equal(collection.Tracks, []string{"move-me"}) {
					t.Errorf("destination did not contain the playlist: %+v", collection)
				}
				if scenario == "failed location save" {
					return false, errors.New("write refused")
				}
				return scenario != "durability unconfirmed", nil
			})
			succeeded := scenario == "success" || scenario == "durability unconfirmed"
			if (err == nil) != succeeded || published != (succeeded || scenario == "failed location save") {
				t.Errorf("move returned %v, published=%t", err, published)
			}
			if data, err := os.ReadFile(filepath.Join(from, "notes.txt")); err != nil || string(data) != "not a download" {
				t.Errorf("unrelated source file changed: %q, %v", data, err)
			}
			if scenario == "durability unconfirmed" && !kept {
				t.Error("the move deleted its source without a durable location")
			}
			_, err = os.Stat(filepath.Join(from, track.File))
			retired := succeeded && !kept
			if retired && !errors.Is(err, os.ErrNotExist) || !retired && err != nil {
				t.Errorf("source track has incorrect state after move: %v", err)
			}
			switch scenario {
			case "occupied destination":
				if data, err := os.ReadFile(filepath.Join(to, "notes.txt")); err != nil || string(data) != "already there" {
					t.Errorf("occupied destination changed: %q, %v", data, err)
				}
			case "success", "durability unconfirmed":
				wrong := Folder{Dir: to, ID: "another-library"}
				if err := wrong.Write(t.Context(), Library{}); !errors.Is(err, ErrUnavailable) {
					t.Errorf("opened another library for writing: %v", err)
				}
				if err := wrong.Remove(t.Context(), []string{track.File}); !errors.Is(err, ErrUnavailable) {
					t.Errorf("removed files from another library: %v", err)
				}
				if scenario == "durability unconfirmed" {
					reset := Folder{Dir: from}
					publish := func(string) (bool, error) { return true, nil }
					if _, err := Move(t.Context(), target, reset, lib, "wrong fingerprint", publish); err == nil {
						t.Error("reset should not replace an unrelated library")
					}
					if _, err := Move(t.Context(), target, reset, lib, retainedDefault, publish); err != nil {
						t.Fatalf("reset with the retained default: %v", err)
					}
					copies, err := filepath.Glob(filepath.Join(from, "Meiro Downloads backup *", track.File))
					if err != nil || len(copies) != 1 {
						t.Errorf("the former default was not kept as a backup: %q, %v", copies, err)
					}
					if got, err := reset.Open(t.Context()); err != nil || len(got.Songs) != 1 {
						t.Errorf("default library not restored: %+v, %v", got.Songs, err)
					}
				}
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
	folder := Folder{Dir: dir}
	if err := folder.Write(t.Context(), lib); err != nil {
		t.Fatal(err)
	}
	if _, err := folder.Open(t.Context()); err == nil {
		t.Error("opened a custom library that references a file outside it")
	}
}

func TestLibrarySnapshotKeepsThePreviousPlaylist(t *testing.T) {
	var lib Library
	lib.AddCollection(playlist("VLPLmove", "first"))
	snapshot := lib
	lib.SetCollectionTracks("VLPLmove", []string{"first", "second"})
	if collection, _ := snapshot.Collection("VLPLmove"); !slices.Equal(collection.Tracks, []string{"first"}) {
		t.Errorf("pending index snapshot changed: %q", collection.Tracks)
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

// Reopening removes only temp files in Meiro's namespace, not other audio.
func TestOpenKeepsUnrelatedFilesAndSweepsTemps(t *testing.T) {
	dir := t.TempDir()
	listed := fmt.Sprintf("%x.webm", sha256.Sum256([]byte("v1")))
	unlisted := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.m4a"
	temp := ".meiro-audio-AAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, name := range []string{listed, unlisted, temp, "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var lib Library
	lib.Add(Song{Item: youtube.MusicItem{VideoID: "v1", Title: "One"}, File: listed})
	lib.AddCollection(playlist("VLPLa", "v1"))
	folder := Folder{Dir: dir}
	if err := folder.Write(t.Context(), lib); err != nil {
		t.Fatal(err)
	}

	read, err := folder.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := read.Song("v1"); !ok || s.File != listed || s.Item.Title != "One" {
		t.Fatalf("song after reading = %+v, %v", s, ok)
	}
	if c, ok := read.Collection("VLPLa"); !ok || !slices.Equal(c.Tracks, []string{"v1"}) {
		t.Fatalf("playlist after reading = %+v, %v", c, ok)
	}
	for name, want := range map[string]bool{listed: true, unlisted: true, temp: false, "notes.txt": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}
