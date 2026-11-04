package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/elianiva/meiro/youtube"
)

// songResolveTimeout bounds one search for the song that plays in place of a
// video. The answer is cached, so the bound is paid at most once per video.
const songResolveTimeout = 30 * time.Second

// searchSong finds the song that plays in place of video, by searching
// YouTube Music for its title and artist and picking the best song from the
// answers. It returns the zero item when the search found no acceptable
// song, and an error when YouTube could not be asked at all. It runs off the
// main thread.
func (a *app) searchSong(ctx context.Context, client *youtube.Client, video youtube.MusicItem) (youtube.MusicItem, error) {
	result, err := client.Search(ctx, songSearchQuery(video), youtube.SearchOptions{})
	if err != nil {
		return youtube.MusicItem{}, err
	}
	songs := make([]youtube.MusicItem, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Kind == youtube.KindTrack && item.VideoID != "" {
			songs = append(songs, item)
		}
	}
	song, ok := bestSongFor(video, songs)
	if !ok {
		return youtube.MusicItem{}, nil
	}
	return song, nil
}

// songSearchQuery is what a search for the song version of a track asks: its
// title, and its artist when one is known.
func songSearchQuery(video youtube.MusicItem) string {
	query := strings.TrimSpace(video.Title)
	if artist := songQueryArtist(video); artist != "" {
		query += " " + artist
	}
	return query
}

// songQueryArtist is the artist a search for video should name: the item's
// own, or the name its subtitle leads with.
func songQueryArtist(video youtube.MusicItem) string {
	if video.Artist != "" {
		return video.Artist
	}
	if first, _, _ := strings.Cut(video.Artists, ","); first != "" {
		return strings.TrimSpace(first)
	}
	return subtitleLead(video)
}

// subtitleLead is the first name of a track's subtitle, as the "Someone" of
// "Someone • 3:42"; empty when the subtitle leads with the kind of the row,
// such as "Song", or with a length, as it does in a row that names no artist.
func subtitleLead(item youtube.MusicItem) string {
	lead := strings.TrimSpace(strings.SplitN(item.Subtitle, "•", 2)[0])
	if isSongKindName(lead) || parseDuration(lead) > 0 || isCountPhrase(lead) {
		return ""
	}
	return lead
}

// isCountPhrase reports whether a name ends in a tally, as "1.4M views"
// does: it counts the video, not the artist. A tally leads with its
// number, so a name of one word, as an artist called Views would be, is
// still a name.
func isCountPhrase(name string) bool {
	if strings.IndexByte(name, ' ') < 0 {
		return false
	}
	last := name[strings.LastIndexByte(name, ' ')+1:]
	switch strings.ToLower(last) {
	case "views", "listeners", "likes", "subscribers", "suscriptores":
		return true
	}
	return false
}

// isSongKindName reports whether a subtitle leads with the kind of the row
// rather than an artist.
func isSongKindName(name string) bool {
	switch name {
	case "Song", "Video", "Music", "Track", "Single", "Album", "Artist", "Playlist", "Episode", "Podcast":
		return true
	}
	return false
}

// bestSongFor is the song among songs that plays the same track as video:
// one that folds to the same title, names the same artist, and is about as
// long. The song whose length is closest to the video's wins.
func bestSongFor(video youtube.MusicItem, songs []youtube.MusicItem) (youtube.MusicItem, bool) {
	title := songTitleFold(video.Title)
	if title == "" {
		return youtube.MusicItem{}, false
	}
	videoLength := parseDuration(video.Duration)
	wantArtists := songArtistFolds(video)
	var best youtube.MusicItem
	var bestGap time.Duration
	found := false
	for _, song := range songs {
		if song.VideoID == "" || song.VideoID == video.VideoID || songTitleFold(song.Title) != title {
			continue
		}
		if length := parseDuration(song.Duration); videoLength > 0 && length > 0 && (videoLength-length).Abs() > sameSongTolerance {
			continue
		}
		if len(wantArtists) > 0 && !songArtistMatches(song, wantArtists) {
			continue
		}
		gap := songLengthGap(videoLength, parseDuration(song.Duration))
		if !found || gap < bestGap {
			best, bestGap, found = song, gap, true
		}
	}
	return best, found
}

// songLengthGap is how far apart two known lengths are; an unknown length, on
// either side, ties with any other.
func songLengthGap(a, b time.Duration) time.Duration {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a - b).Abs()
}

// songTitleFold is a title in the form uploads of the same song share:
// lowercase, without punctuation or spacing, and without a trailing phrase
// naming the kind of upload.
func songTitleFold(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	for {
		shortened := title
		for _, label := range videoTitleLabels {
			shortened = strings.TrimSuffix(strings.TrimSpace(shortened), label)
		}
		if shortened == title {
			break
		}
		title = shortened
	}
	return normalizeName(title)
}

// videoTitleLabels are the trailing phrases that mark a video upload, which
// the song version of the same track does without.
var videoTitleLabels = []string{
	"(official music video)", "(official video)", "(music video)",
	"(official lyric video)", "(lyric video)", "(lyrics)",
	"(official visualizer)", "(visualizer)", "(official audio)", "(audio)",
	"official music video", "official video", "official lyric video",
	"lyric video", "music video",
}

// songArtistFolds are the names item carries as its artists, folded. The
// artist the item names and the one its subtitle leads with count.
func songArtistFolds(item youtube.MusicItem) []string {
	var names []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, normalizeName(name))
		}
	}
	add(item.Artist)
	for _, name := range strings.Split(item.Artists, ",") {
		add(name)
	}
	if item.Artist == "" && item.Artists == "" {
		add(subtitleLead(item))
	}
	return names
}

// songArtistMatches reports whether song names one of the artists.
func songArtistMatches(song youtube.MusicItem, want []string) bool {
	for _, have := range songArtistFolds(song) {
		if slices.Contains(want, have) {
			return true
		}
	}
	return false
}

// songFor is item itself, or the song the cache knows plays in its place.
// known says the cache holds an answer for the video; a song without a
// video ID is the answer that it has no song version.
func (a *app) songFor(item youtube.MusicItem) (song youtube.MusicItem, known bool) {
	if !item.IsVideo() {
		return item, true
	}
	return a.songs.Get(item.VideoID)
}

// rememberSong keeps the song that plays in place of videoID, so the video
// is never looked up again. The zero item records that it has no song.
func (a *app) rememberSong(videoID string, song youtube.MusicItem) {
	a.songs.Put(videoID, song)
}

// swapToSong makes the queue hold the song that plays in place of the video
// the listener chose, so the queue, the current track and audio resolution
// all agree on what is playing.
func (a *app) swapToSong(videoID string, song youtube.MusicItem) {
	a.queue.songOf(videoID, song)
	if a.current.VideoID == videoID {
		a.current = song
		// The play being followed was built from the video's raw title,
		// before the song was known; the song is what Last.fm should hear
		// of now.
		if l := a.scrobble.listen; l != nil {
			l.Track = scrobbleTrack(a.withKnownCredits(song))
		}
	}
	// A video the listener queued keeps its chosen mark under the song that
	// plays in its place.
	if _, chosen := a.scrobble.queued[videoID]; chosen {
		delete(a.scrobble.queued, videoID)
		a.rememberQueued(song.VideoID)
	}
	a.page.rowsDirty = true
}
