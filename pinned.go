package main

import (
	"slices"

	"github.com/elianiva/meiro/internal/settings"
)

// playlistPinned reports whether the playlist with this ID is pinned to the
// navigation rail.
func (a *app) playlistPinned(id string) bool {
	for _, p := range a.settings.PinnedPlaylists {
		if p.ID == id {
			return true
		}
	}
	return false
}

// setPinned pins a playlist to the navigation rail or takes it off, keeping
// the order the playlists were pinned in and the heading the rail shows
// before the page loads.
func (a *app) setPinned(id string, pin bool, title, art string) {
	ps := slices.DeleteFunc(a.settings.PinnedPlaylists, func(p settings.PinnedPlaylist) bool {
		return p.ID == id
	})
	if pin {
		ps = append(ps, settings.PinnedPlaylist{ID: id, Title: title, Art: art})
	}
	a.settings.PinnedPlaylists = ps
	a.saveSettings()
}

// refreshPinned updates a pinned playlist's title and art, so a renamed
// playlist or a new cover shows up in the rail when its page loads.
func (a *app) refreshPinned(id, title, art string) {
	if id == "" || title == "" {
		return
	}
	changed := false
	for i, p := range a.settings.PinnedPlaylists {
		newArt := p.Art
		if art != "" {
			newArt = art
		}
		if p.ID != id || (p.Title == title && p.Art == newArt) {
			continue
		}
		a.settings.PinnedPlaylists[i].Title, a.settings.PinnedPlaylists[i].Art = title, newArt
		changed = true
	}
	if changed {
		a.saveSettings()
	}
}
