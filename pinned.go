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
