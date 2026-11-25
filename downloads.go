package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/offline"
	"github.com/elianiva/meiro/m3"
	"github.com/elianiva/meiro/youtube"
)

// downloadTimeout bounds finding and saving one track: a long podcast
// episode takes a while even at full speed.
const downloadTimeout = 30 * time.Minute

// downloads is the music saved to play offline, and the tracks on their way
// there, one at a time. It belongs to the main thread.
type downloads struct {
	// dir is the folder that holds the files and their index; empty when
	// there is none, which turns downloading off.
	dir    string
	lib    offline.Library
	writer offline.Writer
	files  atomic.Int32

	defaultDir, locationFile string
	locationID, locationErr  string
	defaultBackup            string
	available, loaded        bool
	choosing                 bool
	moveDone                 chan struct{}
	move, probe              task
	pickFolder               func(string) (string, error)
	// queue is the tracks waiting, and active the one being downloaded.
	queue      []pendingSong
	active     pendingSong
	job        task
	installing bool
	// version counts the changes to the library, so the Downloads page
	// knows when to read it again.
	version int
}

// pendingSong is a track waiting to be downloaded. single says the user
// asked for it by itself rather than with an album or a playlist.
type pendingSong struct {
	item   youtube.MusicItem
	single bool
}

// openDownloads opens the local default folder. Only the default can start
// with no index; a custom folder must already hold its library.
func (a *app) openDownloads(dir string) {
	d := &a.downloads
	d.dir = dir
	d.version++
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.locationErr = "Could not open the download folder: " + err.Error()
		return
	}
	lib, err := d.folder().Open(context.Background())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		d.locationErr = "Could not read the download library: " + err.Error()
		return
	}
	d.lib = lib
	d.available, d.loaded = true, true
}

func (d *downloads) writable() bool { return d.available && !d.move.busy() && !d.installing }

type downloadedAudio struct {
	folder offline.Folder
	file   string
}

func (a *app) useDownloadAudio(videoID string) (downloadedAudio, func()) {
	d := &a.downloads
	song, ok := d.lib.Song(videoID)
	if !ok || !d.available || d.move.busy() {
		return downloadedAudio{}, func() {}
	}
	d.files.Add(1)
	return downloadedAudio{folder: d.folder(), file: filepath.Join(d.dir, song.File)}, func() { d.files.Add(-1) }
}

// isDownloaded reports whether a track is downloaded.
func (a *app) isDownloaded(videoID string) bool {
	_, ok := a.downloads.lib.Song(videoID)
	return ok && a.downloads.available
}

// downloading reports whether a track is waiting or being downloaded.
func (a *app) downloading(videoID string) bool {
	d := &a.downloads
	if d.job.busy() && d.active.item.VideoID == videoID {
		return true
	}
	return slices.ContainsFunc(d.queue, func(p pendingSong) bool { return p.item.VideoID == videoID })
}

// canDownload reports whether a song can be downloaded or removed.
func (a *app) canDownload(item youtube.MusicItem) bool {
	return a.downloads.writable() && item.VideoID != "" && item.IsTrack()
}

// downloadSong saves a track to play offline.
func (a *app) downloadSong(item youtube.MusicItem) {
	a.queueDownloads([]youtube.MusicItem{item}, true)
}

// downloadCollection saves the album or playlist item names, and every track
// of it, to play offline. Tracks without artwork of their own take the
// collection's.
func (a *app) downloadCollection(item youtube.MusicItem, tracks []youtube.MusicItem) {
	d := &a.downloads
	if !d.writable() || len(tracks) == 0 {
		return
	}
	songs := make([]youtube.MusicItem, 0, len(tracks))
	ids := make([]string, 0, len(tracks))
	for _, track := range tracks {
		if track.VideoID == "" || slices.Contains(ids, track.VideoID) {
			continue
		}
		if track.Thumbnail == "" {
			track.Thumbnail = item.Thumbnail
		}
		songs = append(songs, track)
		ids = append(ids, track.VideoID)
	}
	d.lib.AddCollection(offline.Collection{Item: item, Tracks: ids})
	a.saveDownloads()
	a.queueDownloads(songs, false)
}

// syncCollectionDownload downloads the tracks a downloaded album or playlist
// gained since it was downloaded. It only adds to the saved list, so the part
// of a playlist that has loaded can be synced before the rest arrives.
func (a *app) syncCollectionDownload(p *pageData, r *route, id string) {
	d := &a.downloads
	if !d.writable() || (r.id != routeAlbum && r.id != routePlaylist) || p.feed.loading || p.feed.err != "" {
		return
	}
	collection, ok := d.lib.Collection(id)
	if !ok {
		return
	}
	tracks := pageTracks(p.feed)
	ids := make([]string, 0, len(tracks)+len(collection.Tracks))
	var added []youtube.MusicItem
	for _, track := range tracks {
		if track.VideoID == "" || !track.IsTrack() || slices.Contains(ids, track.VideoID) {
			continue
		}
		ids = append(ids, track.VideoID)
		if slices.Contains(collection.Tracks, track.VideoID) {
			continue
		}
		if track.Thumbnail == "" {
			track.Thumbnail = collection.Item.Thumbnail
		}
		added = append(added, track)
	}
	if len(added) == 0 {
		return
	}
	// The playlist's own order, then any saved track it no longer lists.
	for _, videoID := range collection.Tracks {
		if !slices.Contains(ids, videoID) {
			ids = append(ids, videoID)
		}
	}
	d.lib.SetCollectionTracks(id, ids)
	a.saveDownloads()
	a.queueDownloads(added, false)
}

// pageTracks is the songs an album or playlist page lists, in its own order,
// without the suggestions under a playlist.
func pageTracks(feed pageState) []youtube.MusicItem {
	sections := feed.sections
	if len(sections) == 0 {
		sections = []youtube.MusicSection{{Items: feed.items}}
	}
	var tracks []youtube.MusicItem
	for _, section := range sections {
		for _, item := range section.Items {
			if item.IsTrack() {
				tracks = append(tracks, item)
			}
		}
	}
	return tracks
}

// queueDownloads puts the tracks that are not downloaded yet in the queue.
func (a *app) queueDownloads(items []youtube.MusicItem, single bool) {
	d := &a.downloads
	if !d.writable() {
		return
	}
	for _, item := range items {
		if !a.canDownload(item) {
			continue
		}
		if song, ok := d.lib.Song(item.VideoID); ok {
			if single && !song.Single {
				song.Single = true
				d.lib.Add(song)
				a.saveDownloads()
			}
			continue
		}
		if i := slices.IndexFunc(d.queue, func(p pendingSong) bool { return p.item.VideoID == item.VideoID }); i >= 0 {
			d.queue[i].single = d.queue[i].single || single
			continue
		}
		if d.job.busy() && d.active.item.VideoID == item.VideoID {
			d.active.single = d.active.single || single
			continue
		}
		d.queue = append(d.queue, pendingSong{item: item, single: single})
	}
	a.nextDownload()
}

// nextDownload starts the download of the track first in the queue, unless
// one is under way.
func (a *app) nextDownload() {
	d := &a.downloads
	if !d.writable() || d.job.busy() || len(d.queue) == 0 {
		return
	}
	d.active = d.queue[0]
	d.queue = slices.Delete(d.queue, 0, 1)
	item, folder, streams := d.active.item, d.folder(), a.streams
	d.files.Add(1)
	startTask(a, &d.job, downloadTimeout, func(ctx context.Context) (string, error) {
		defer d.files.Add(-1)
		if folder.ID != "" && !folder.Available(ctx) {
			return "", offline.ErrUnavailable
		}
		url, _, err := streams.Get(ctx, item.VideoID)
		if err != nil {
			return "", err
		}
		file, err := folder.Download(ctx, item.VideoID, url)
		if err != nil {
			streams.Forget(item.VideoID)
		}
		return file, downloadError(ctx, folder, err)
	}, func(file string, err error) {
		if err != nil {
			a.finishDownload(err)
			return
		}
		lib := d.lib
		lib.Add(offline.Song{Item: item, File: file, Single: d.active.single})
		d.installing = true
		startTask(a, &d.job, noTimeout, func(ctx context.Context) (struct{}, error) {
			d.writer.Wait()
			return struct{}{}, downloadError(ctx, folder, folder.Write(ctx, lib))
		}, func(_ struct{}, err error) {
			d.installing = false
			if err == nil {
				d.lib = lib
				d.version++
			}
			a.finishDownload(err)
		})
	})
}

func downloadError(ctx context.Context, folder offline.Folder, err error) error {
	if err != nil && folder.ID != "" && ctx.Err() == nil && !folder.Available(ctx) {
		return offline.ErrUnavailable
	}
	return err
}

func (a *app) finishDownload(err error) {
	d := &a.downloads
	p := d.active
	d.active = pendingSong{}
	if errors.Is(err, offline.ErrUnavailable) && d.custom() {
		d.queue = slices.Insert(d.queue, 0, p)
		d.setAvailable(false)
		return
	}
	if err != nil {
		log.Printf("downloads: could not download video_id=%s: %v", p.item.VideoID, err)
		a.notice = "Could not download “" + p.item.Title + "”"
	}
	a.nextDownload()
}

// removeDownload deletes a downloaded track, or stops it from being
// downloaded.
func (a *app) removeDownload(videoID string) {
	d := &a.downloads
	if !d.writable() {
		return
	}
	d.queue = slices.DeleteFunc(d.queue, func(p pendingSong) bool { return p.item.VideoID == videoID })
	if d.job.busy() && d.active.item.VideoID == videoID {
		d.job.stop()
		d.active = pendingSong{}
		a.nextDownload()
	}
	if file, ok := d.lib.Remove(videoID); ok {
		a.deleteDownloadFiles([]string{file})
		a.saveDownloads()
	}
}

// removeCollectionDownload deletes a downloaded album or playlist, with the
// tracks that were downloaded only for it, and stops the ones still waiting.
func (a *app) removeCollectionDownload(id string) {
	d := &a.downloads
	if !d.writable() {
		return
	}
	collection, ok := d.lib.Collection(id)
	if !ok {
		return
	}
	files := d.lib.RemoveCollection(id)
	// A track waiting only for this collection is not wanted any more.
	unwanted := func(p pendingSong) bool {
		return !p.single && slices.Contains(collection.Tracks, p.item.VideoID) && !d.lib.InCollection(p.item.VideoID)
	}
	d.queue = slices.DeleteFunc(d.queue, unwanted)
	if d.job.busy() && unwanted(d.active) {
		d.job.stop()
		d.active = pendingSong{}
		a.nextDownload()
	}
	a.deleteDownloadFiles(files)
	a.saveDownloads()
}

// collectionDownload says how much of a collection's tracks are downloaded,
// whether it was downloaded at all, and whether any of them are on their way.
func (a *app) collectionDownload(id string) (done, total int, saved, busy bool) {
	d := &a.downloads
	collection, ok := d.lib.Collection(id)
	if !ok {
		return 0, 0, false, false
	}
	for _, videoID := range collection.Tracks {
		if _, ok := d.lib.Song(videoID); ok {
			done++
		} else if a.downloading(videoID) {
			busy = true
		}
	}
	return done, len(collection.Tracks), true, busy
}

// deleteDownloadFiles deletes the files of removed tracks, off the main
// thread.
func (a *app) deleteDownloadFiles(files []string) {
	d := &a.downloads
	folder := d.folder()
	if len(files) == 0 || folder.Dir == "" {
		return
	}
	d.files.Add(1)
	a.run(func() {
		defer d.files.Add(-1)
		if err := folder.Remove(context.Background(), files); err != nil {
			log.Printf("downloads: removing files: %v", err)
		}
	})
}

// saveDownloads writes the library's index, and tells the Downloads page.
func (a *app) saveDownloads() {
	d := &a.downloads
	d.version++
	d.writer.Save(d.folder(), d.lib)
}

// offlinePage is the downloaded copy of an album or a playlist, for its page
// and its play button while the network is out of reach.
func (a *app) offlinePage(id string) (*youtube.BrowseResult, bool) {
	d := &a.downloads
	collection, ok := d.lib.Collection(id)
	if !ok || !d.available {
		return nil, false
	}
	return &youtube.BrowseResult{
		Title:     collection.Item.Title,
		Thumbnail: collection.Item.Thumbnail,
		Items:     d.lib.Tracks(collection),
	}, true
}

// syncDownloadsPage fills the Downloads page with what is downloaded, when
// that changed since the page last read it.
func (a *app) syncDownloadsPage() {
	d := &a.downloads
	p := a.page
	if p.downloads == d.version && !p.loadedAt.IsZero() {
		return
	}
	p.downloads, p.loadedAt, p.rowsDirty = d.version, time.Now(), true
	if d.locationFile != "" && !d.available {
		p.feed = pageState{err: d.unavailableMessage()}
		return
	}
	var sections []youtube.MusicSection
	if len(d.lib.Collections) > 0 {
		items := make([]youtube.MusicItem, len(d.lib.Collections))
		for i, collection := range d.lib.Collections {
			items[i] = collection.Item
		}
		sections = append(sections, youtube.MusicSection{Title: "Albums and playlists", Items: items})
	}
	if len(d.lib.Songs) > 0 {
		items := make([]youtube.MusicItem, len(d.lib.Songs))
		for i, song := range d.lib.Songs {
			items[i] = song.Item
		}
		sections = append(sections, youtube.MusicSection{Title: "Songs", Items: items})
	}
	p.feed = pageState{sections: sections}
}

// pageDownloadState is what the download control for the page shows.
type pageDownloadState struct {
	label          string
	icon           *ui.SVG
	kind           m3.ButtonKind
	disabled       bool
	busy, complete bool
}

// pageDownload returns the state of the download control for the page shown,
// the collection its ID names, and whether the page offers one at all.
func (a *app) pageDownload() (pageDownloadState, string, bool) {
	r, id := a.route()
	if a.downloads.dir == "" || r.id != routeAlbum && r.id != routePlaylist {
		return pageDownloadState{}, "", false
	}
	done, total, saved, busy := a.collectionDownload(id)
	complete := saved && done == total
	state := pageDownloadState{label: "Download", icon: m3.IconDownload, kind: m3.Tonal, busy: busy, complete: complete}
	switch {
	case busy:
		state.label = "Downloading " + strconv.Itoa(done) + "/" + strconv.Itoa(total)
		state.kind = m3.Outlined
	case complete:
		state.label, state.icon, state.kind = "Downloaded", m3.IconDownloadDone, m3.Outlined
	}
	// A playlist's songs past its first page are still on their way while
	// the rest of it loads, and a download now would leave them out.
	state.disabled = !a.downloads.writable() || !busy && !complete && (len(a.page.playable) == 0 || a.pageSimilar.busy() || a.page.feed.loading)
	return state, id, true
}

// togglePageDownload downloads the page shown, or removes its download when
// one is done or under way.
func (a *app) togglePageDownload(id string, state pageDownloadState) {
	if state.busy || state.complete {
		a.removeCollectionDownload(id)
		return
	}
	r, _ := a.route()
	kindOf := youtube.KindPlaylist
	if r.id == routeAlbum {
		kindOf = youtube.KindAlbum
	}
	d := a.page.detail
	a.downloadCollection(youtube.MusicItem{BrowseID: id, Title: d.title, Subtitle: d.subtitle, Thumbnail: d.art, Kind: kindOf}, a.page.playable)
}

// downloadButton is the hero's button that downloads an album or a playlist,
// says how far that has got, and removes the download again.
func (a *app) downloadButton(c *ui.Context) {
	state, id, ok := a.pageDownload()
	if !ok {
		return
	}
	if m3.Button(c, m3.ButtonSpec{Label: state.label, Icon: state.icon, Kind: state.kind, Size: a.heroButtonSize(), Disabled: state.disabled, Key: "hero-download"}).Clicked() {
		a.togglePageDownload(id, state)
	}
}
