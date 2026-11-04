package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/elianiva/meiro/youtube"
)

// videoOf is a music video track, as a playlist, shelf or search can offer
// one, by Someone.
func videoOf(title, id, duration string) youtube.MusicItem {
	return youtube.MusicItem{
		VideoID: id, Title: title, Artist: "Someone",
		Subtitle: "Someone • " + duration, Duration: duration,
		Kind: youtube.KindVideo,
	}
}

// noArtistVideo is a music video whose row names no artist at all.
func noArtistVideo(title, duration string) youtube.MusicItem {
	return youtube.MusicItem{
		VideoID: "video-n", Title: title, Subtitle: duration, Duration: duration,
		Kind: youtube.KindVideo,
	}
}

// TestPlayingAVideoPlaysItsSong checks that a video starts its song version:
// the search finds the song, and the queue, the audio and the current track
// all become the song's.
func TestPlayingAVideoPlaysItsSong(t *testing.T) {
	a, r := playbackApp(t)
	searches.Store(0)
	video := videoOf("Searched Song", "video-1", "3:42")

	a.play(video, []youtube.MusicItem{video})
	if a.current.VideoID != "song-id" {
		t.Fatalf("playing a video started %q, want its song song-id", a.current.VideoID)
	}
	if a.queue.at(0).VideoID != "song-id" {
		t.Errorf("the queue holds %q, want the song", a.queue.at(0).VideoID)
	}
	if !r.asked("song-id") || r.asked("video-1") {
		t.Errorf("audio was resolved for %v, want the song only", r.ids)
	}
	if searches.Load() != 1 {
		t.Errorf("%d searches, want one for the video", searches.Load())
	}
}

// TestPlayingAVideoAgainAsksForItsSongOnce checks that the song found for a
// video is remembered: the second play asks no one.
func TestPlayingAVideoAgainAsksForItsSongOnce(t *testing.T) {
	a, _ := playbackApp(t)
	searches.Store(0)
	video := videoOf("Searched Song", "video-1", "3:42")
	a.play(video, []youtube.MusicItem{video})
	if got := searches.Load(); got != 1 {
		t.Fatalf("%d searches for the first play, want 1", got)
	}

	searches.Store(0)
	a.play(video, []youtube.MusicItem{video})
	if a.current.VideoID != "song-id" {
		t.Fatalf("the second play started %q, want the song", a.current.VideoID)
	}
	if got := searches.Load(); got != 0 {
		t.Errorf("%d searches for the second play, want none: the song was cached", got)
	}
}

// TestPlayingAVideoWithNoSongPlaysTheVideo checks the fallback: a video whose
// search finds no matching song plays itself, and the no-song answer is kept,
// so it is not asked for again.
func TestPlayingAVideoWithNoSongPlaysTheVideo(t *testing.T) {
	a, r := playbackApp(t)
	searches.Store(0)
	video := videoOf("No Match Song", "video-2", "3:42")

	a.play(video, []youtube.MusicItem{video})
	if a.current.VideoID != "video-2" {
		t.Fatalf("a video without a song played %q, want the video itself", a.current.VideoID)
	}
	if !r.asked("video-2") {
		t.Errorf("the video's own audio was never resolved")
	}
	if searches.Load() != 1 {
		t.Errorf("%d searches, want one", searches.Load())
	}

	searches.Store(0)
	a.play(video, []youtube.MusicItem{video})
	if a.current.VideoID != "video-2" {
		t.Fatalf("the second play started %q, want the video", a.current.VideoID)
	}
	if got := searches.Load(); got != 0 {
		t.Errorf("%d searches for the second play, want none: no-song is cached too", got)
	}
}

// TestPlayingAVideoWhoseSongLengthMismatchesKeepsTheVideo checks that a song
// with the same title and artist but a different length is not taken for the
// video's song.
func TestPlayingAVideoWhoseSongLengthMismatchesKeepsTheVideo(t *testing.T) {
	a, r := playbackApp(t)
	video := videoOf("Duration Song", "video-3", "3:42")

	a.play(video, []youtube.MusicItem{video})
	if a.current.VideoID != "video-3" {
		t.Fatalf("a 6:00 song played %q for a 3:42 video, want the video", a.current.VideoID)
	}
	if !r.asked("video-3") {
		t.Errorf("the video's own audio was never resolved")
	}
}

// TestQueueingAVideoAddsItsSong checks the queue gets the song version of a
// video, so the cover and title the queue shows are the song's, and a second
// queueing of the same video is answered from the cache.
func TestQueueingAVideoAddsItsSong(t *testing.T) {
	a, _ := playbackApp(t)
	searches.Store(0)
	base := tracks("a")
	a.play(base[0], base)
	video := videoOf("Searched Song", "video-1", "3:42")

	a.enqueue(video, false)
	if got := ids(a.queue.items); !slices.Equal(got, []string{"a", "song-id"}) {
		t.Fatalf("queue = %v, want a then the song", got)
	}
	a.enqueue(video, true)
	if got := ids(a.queue.items); len(got) != 3 || got[1] != "song-id" {
		t.Fatalf("queue = %v, want the song again next", got)
	}
	if a.queue.playNext != "song-id" {
		t.Errorf("play next marks %q, want the song", a.queue.playNext)
	}
	if got := searches.Load(); got != 1 {
		t.Errorf("%d searches, want one: the second add came from the cache", got)
	}
}

// TestAddingAVideoToAPlaylistAddsItsSong checks that a video offered to a
// playlist is added as its song, so the playlist holds and shows the track
// that plays.
func TestAddingAVideoToAPlaylistAddsItsSong(t *testing.T) {
	a := signedInApp(t)
	video := videoOf("Searched Song", "video-1", "3:42")
	playlist := youtube.MusicItem{Title: "Hype", PlaylistID: "PLhype", BrowseID: "VLPLhype", Kind: youtube.KindPlaylist}
	a.addToPlaylist = &addToPlaylistState{open: true, item: video, playlists: []youtube.MusicItem{playlist}}

	a.choosePlaylist(playlist)
	write := written()
	if !strings.HasSuffix(write.endpoint, "/browse/edit_playlist") {
		t.Fatalf("write = %#v", write)
	}
	added := write.body["actions"].([]any)[0].(map[string]any)["addedVideoId"]
	if added != "song-id" {
		t.Errorf("added %v to the playlist, want the song song-id", added)
	}
}

// TestBestSongFor is the matching table: which search result is taken for a
// video's song version, by title, artist and length.
func TestBestSongFor(t *testing.T) {
	video := videoOf("Searched Song", "video-1", "3:42")
	song := func(id, title, artist, duration string) youtube.MusicItem {
		return youtube.MusicItem{VideoID: id, Title: title, Artist: artist, Duration: duration, Kind: youtube.KindTrack}
	}
	tests := []struct {
		name   string
		video  youtube.MusicItem
		songs  []youtube.MusicItem
		wantID string // the song picked; empty when none matches
	}{
		{"the exact song matches", video, []youtube.MusicItem{song("song-id", "Searched Song", "Someone", "3:42")}, "song-id"},
		{"an official video title folds to its song", videoOf("Searched Song (Official Video)", "video-1", "3:42"), []youtube.MusicItem{song("song-id", "Searched Song", "Someone", "3:42")}, "song-id"},
		{"a different title is not the song", video, []youtube.MusicItem{song("song-id", "Other Song", "Someone", "3:42")}, ""},
		{"another artist is not the song", video, []youtube.MusicItem{song("song-id", "Searched Song", "Somebody Else", "3:42")}, ""},
		{"a much longer upload is not the song", video, []youtube.MusicItem{song("song-id", "Searched Song", "Someone", "6:00")}, ""},
		{"an unknown video length matches by title and artist", videoOf("Searched Song", "video-1", ""), []youtube.MusicItem{song("song-id", "Searched Song", "Someone", "6:00")}, "song-id"},
		{"a video without an artist matches by title and length", noArtistVideo("Searched Song", "3:42"), []youtube.MusicItem{song("song-id", "Searched Song", "", "3:42")}, "song-id"},
		{"the closest length wins", video, []youtube.MusicItem{
			song("song-far", "Searched Song", "Someone", "3:45"),
			song("song-near", "Searched Song", "Someone", "3:40"),
		}, "song-near"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := bestSongFor(tt.video, tt.songs)
			if got.VideoID != tt.wantID {
				t.Errorf("bestSongFor picked %q, want %q", got.VideoID, tt.wantID)
			}
		})
	}
}
