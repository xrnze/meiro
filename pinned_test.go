package main

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/settings"
)

// pin opens the playlist's hero menu and pins it, or unpins it when it is
// already pinned, as the menu offers then.
func pin(t *testing.T, a *app, tt *ui.Tester, playlist, title string) {
	t.Helper()
	label := "Pin to sidebar"
	if a.playlistPinned(playlist) {
		label = "Unpin from sidebar"
	}
	if err := tt.RightClick("More options for " + title); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
}

// textAt is where a text stands in the window's texts, for asking that the
// rail show things in its order.
func textAt(texts []string, text string) int {
	for i, t := range texts {
		if t == text {
			return i
		}
	}
	return -1
}

func TestPinningAPlaylistPersistsAndUnpins(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	a.saver.Path = filepath.Join(t.TempDir(), "settings.json")
	tt := open(a, "/playlist/VLPL_songs")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")

	want := []settings.PinnedPlaylist{{ID: "VLPL_songs", Title: "Ameiro Playlist", Art: "https://img.test/ameiro.png"}}
	if !slices.Equal(a.settings.PinnedPlaylists, want) {
		t.Fatalf("pin kept %+v, want %+v", a.settings.PinnedPlaylists, want)
	}
	a.saver.Wait()
	if got := settings.Load(a.saver.Path); !slices.Equal(got.PinnedPlaylists, want) {
		t.Fatalf("a restart reads %+v, want %+v", got.PinnedPlaylists, want)
	}

	// The menu of a pinned playlist offers to unpin, and does.
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	if len(a.settings.PinnedPlaylists) != 0 {
		t.Fatalf("unpin left %+v", a.settings.PinnedPlaylists)
	}
	a.saver.Wait()
	if got := settings.Load(a.saver.Path); len(got.PinnedPlaylists) != 0 {
		t.Fatalf("a restart reads the unpinned playlist: %+v", got.PinnedPlaylists)
	}
}

func TestPinnedPlaylistsShowInTheRailBelowDestinationsAndFollowPinOrder(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	tt := open(a, "/playlist/VLPL_songs")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	a.router.Push("/playlist/VLPLhype")
	tt.Frame()
	pin(t, a, tt, "VLPLhype", "Hype")

	if got := pins(a); !slices.Equal(got, []string{"VLPL_songs", "VLPLhype"}) {
		t.Fatalf("pins ordered %v, want first pinned first", got)
	}

	a.router.Push("/home")
	tt.Frame()
	if !tt.HasText("Ameiro Playlist") || !tt.HasText("Hype") {
		t.Fatalf("the rail shows no pinned playlists: %q", tt.Texts())
	}
	texts := tt.Texts()
	if at := textAt(texts, "Ameiro Playlist"); at < 0 || at > textAt(texts, "Hype") {
		t.Errorf("pinned playlists stand %q outside pin order", texts)
	}
	// The pinned section sits between the destinations and the footer, under
	// the divider that separates it from them.
	if downloads, pinned, settings := textAt(texts, "Downloads"), textAt(texts, "Ameiro Playlist"), textAt(texts, "Settings"); downloads < 0 || pinned < 0 || settings < 0 || downloads > pinned || pinned > settings {
		t.Errorf("pinned playlists did not land between the destinations and Settings: %q", texts)
	}

	// The expanded rail offers the same entries, with the same selection.
	a.settings.RailExpanded = true
	tt.Frame()
	if !tt.HasText("Ameiro Playlist") || !tt.HasText("Hype") {
		t.Fatalf("the expanded rail shows no pinned playlists: %q", tt.Texts())
	}
}

func TestAClickOnAPinnedPlaylistNavigatesAndStaysSelected(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	tt := open(a, "/playlist/VLPL_songs")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	a.router.Push("/home")
	tt.Frame()

	if err := tt.Click("Ameiro Playlist"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.router.Path() != "/playlist/VLPL_songs" {
		t.Fatalf("a click on a pinned playlist went to %q", a.router.Path())
	}

	// The entry is the selected destination while its page shows, so a click
	// on it has nothing to open, as the current tab's does.
	before := a.router.Location()
	if err := tt.Click("Ameiro Playlist"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.router.Location() != before {
		t.Fatalf("a click on the page's own pinned entry moved to %q", a.router.Location())
	}
}

// Deleting a pinned playlist takes the pin off with the playlist.
func TestDeletingAPinnedPlaylistUnpinsIt(t *testing.T) {
	resetFakes()
	a := signedInApp(t)
	tt := open(a, "/playlist/VLPL_songs")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	if err := tt.RightClick("More options for Ameiro Playlist"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Delete playlist"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if playlistDeletes.Load() != 1 {
		t.Fatalf("the answer wrote %d deletes", playlistDeletes.Load())
	}
	if len(a.settings.PinnedPlaylists) != 0 {
		t.Fatalf("the deleted playlist stayed pinned: %+v", a.settings.PinnedPlaylists)
	}
}

// A pinned playlist whose page loads brings its pin's title and art up to
// date, so a renamed playlist shows its new name in the rail.
func TestALoadedPlaylistPageUpdatesItsPinnedTitle(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	a.setPinned("VLPL_songs", true, "Old Name", "")
	tt := open(a, "/playlist/VLPL_songs")
	tt.Frame()
	want := []settings.PinnedPlaylist{{ID: "VLPL_songs", Title: "Ameiro Playlist", Art: "https://img.test/ameiro.png"}}
	if !slices.Equal(a.settings.PinnedPlaylists, want) {
		t.Fatalf("the loaded page kept the pin %+v, want %+v", a.settings.PinnedPlaylists, want)
	}
	a.router.Push("/home")
	tt.Frame()
	if !tt.HasText("Ameiro Playlist") || tt.HasText("Old Name") {
		t.Fatalf("the rail shows a stale pin: %q", tt.Texts())
	}

	// A pin stored by its ID alone, because the page had not loaded when it
	// was pinned, fills its title in the same way.
	a.setPinned("VLPLhype", true, "", "")
	a.router.Push("/playlist/VLPLhype")
	tt.Frame()
	if a.playlistPinned("VLPLhype") {
		for _, p := range a.settings.PinnedPlaylists {
			if p.ID == "VLPLhype" && p.Title != "Hype" {
				t.Fatalf("the ID-only pin kept no title: %+v", p)
			}
		}
	} else {
		t.Fatal("the ID-only pin vanished")
	}
}

func TestUnpinningTakesThePlaylistOffTheRail(t *testing.T) {
	resetFakes()
	a := newTestApp(t)
	tt := open(a, "/playlist/VLPL_songs")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	pin(t, a, tt, "VLPL_songs", "Ameiro Playlist")
	a.router.Push("/home")
	tt.Frame()
	if tt.HasText("Ameiro Playlist") {
		t.Fatalf("the rail still shows the unpinned playlist: %q", tt.Texts())
	}
}

// pins is the pinned playlists' IDs, in order.
func pins(a *app) []string {
	out := make([]string, len(a.settings.PinnedPlaylists))
	for i, p := range a.settings.PinnedPlaylists {
		out[i] = p.ID
	}
	return out
}
