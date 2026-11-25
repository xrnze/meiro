package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/youtube"
)

// downloadTone serves a short tone for a download to save, and skips a test
// when ffmpeg is not there to make it.
func downloadTone(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tone := filepath.Join(t.TempDir(), "tone.wav")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=duration=5", tone).CombinedOutput(); err != nil {
		t.Fatalf("making a tone: %v: %s", err, out)
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		http.ServeFile(w, r, tone)
	}))
	t.Cleanup(media.Close)
	return media.URL + "/audio"
}

// unreachable fails every request, as a machine with no network does.
type unreachable struct{}

func (unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: no route to host")
}

// audioFiles lists the files in the downloads folder other than its index.
func audioFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".wav" || filepath.Ext(entry.Name()) == ".webm" {
			names = append(names, entry.Name())
		}
	}
	return names
}

func downloadFolderSettings(t *testing.T, a *app) *ui.Tester {
	t.Helper()
	a.router.Push("/settings")
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if err := tt.Click("Search settings"); err != nil {
		t.Fatal(err)
	}
	tt.Type("download folder")
	tt.Frame()
	return tt
}

func TestSettingsShowsDefaultDownloadFolder(t *testing.T) {
	a := newTestApp(t)
	dir := filepath.Join(t.TempDir(), "Downloads")
	a.setupDownloadLocation(filepath.Dir(dir))
	a.downloads.pickFolder = func(string) (string, error) { return "", nil }
	tt := downloadFolderSettings(t, a)
	if !tt.HasText("Download folder") || !tt.HasText(dir) {
		t.Fatalf("Settings should show the current download folder: %q", tt.Texts())
	}
	if err := tt.Click("Choose download folder"); err != nil {
		t.Fatalf("Settings should offer a folder picker: %v", err)
	}
}

func TestChangingDownloadFolderMovesTheOfflineLibrary(t *testing.T) {
	mediaURL := downloadTone(t)
	a, r := playbackApp(t)
	data, selected := t.TempDir(), t.TempDir()
	selected, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	from, to := filepath.Join(data, "Downloads"), filepath.Join(selected, "Meiro Downloads")
	a.setupDownloadLocation(data)
	if err := os.WriteFile(filepath.Join(from, "notes.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.downloads.pickFolder = func(string) (string, error) { return selected, nil }
	r.url = func(string, int) (string, error) { return mediaURL, nil }
	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if err := tt.Click("Download"); err != nil {
		t.Fatal(err)
	}
	a.downloads.writer.Wait()
	if files := audioFiles(t, from); len(files) != 1 {
		t.Fatalf("download before the move: %q", files)
	}
	tt = downloadFolderSettings(t, a)
	if err := tt.Click("Choose download folder"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(to) || len(audioFiles(t, to)) != 1 || len(audioFiles(t, from)) != 0 {
		t.Fatalf("library did not move: %q, origin=%q destination=%q", tt.Texts(), audioFiles(t, from), audioFiles(t, to))
	}

	// Start offline with the selected drive absent, then reconnect it.
	away := filepath.Join(selected, "disconnected")
	if err := os.Rename(to, away); err != nil {
		t.Fatal(err)
	}
	a, r = playbackApp(t)
	a.setupDownloadLocation(data)
	r.url = func(string, int) (string, error) { return "", errors.New("offline") }
	a.account.public = youtube.NewClient(youtube.Options{APIKey: "test", HTTPClient: &http.Client{Transport: unreachable{}}})
	a.router.Push("/downloads")
	tt = ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if !tt.HasText("Download folder is unavailable. Reconnect the drive to use your downloads.") {
		t.Errorf("Downloads should explain why the folder is unavailable: %q", tt.Texts())
	}
	if _, err := os.Stat(to); !errors.Is(err, os.ErrNotExist) || len(audioFiles(t, from)) != 0 {
		t.Errorf("Meiro should not create a replacement library: %v", err)
	}
	if err := os.Mkdir(to, 0o700); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(to, "downloads.json")
	if err := os.WriteFile(index, []byte(`{"songs":[],"collections":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.checkDownloadFolder()
	tt.Frame()
	if !tt.HasText("Download folder is unavailable. Reconnect the drive to use your downloads.") {
		t.Errorf("a different library at the same path should not be accepted: %q", tt.Texts())
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, to); err != nil {
		t.Fatal(err)
	}
	a.checkDownloadFolder() // the periodic drive check
	tt.Frame()
	if err := tt.Click("Album Track One"); err != nil {
		t.Fatalf("reconnected track missing: %v (%q)", err, tt.Texts())
	}
	if a.playErr != "" || r.asked("vid-3") || !a.player.Active() {
		t.Fatalf("moved track did not play offline: %q, resolved=%t", a.playErr, r.asked("vid-3"))
	}
	tt = downloadFolderSettings(t, a)
	if !tt.HasText("Wait for downloads or offline playback to finish before moving the folder.") {
		t.Errorf("the offline decoder should block the move: %q", tt.Texts())
	}
	a.player.Stop()
	tt.Frame()
	if err := tt.Click("Reset download folder"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(from) || len(audioFiles(t, from)) != 1 || len(audioFiles(t, to)) != 0 {
		t.Fatalf("reset did not move the offline library back: %q", tt.Texts())
	}
	if data, err := os.ReadFile(filepath.Join(from, "notes.txt")); err != nil || string(data) != "keep me" {
		t.Errorf("reset changed an unrelated file: %q, %v", data, err)
	}
}

func TestChangingDownloadFolderDoesNotStopStreaming(t *testing.T) {
	a, r := tonesApp(t, 30)
	a.setupDownloadLocation(t.TempDir())
	selected, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.downloads.pickFolder = func(string) (string, error) { return selected, nil }
	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if err := tt.Click("Album Track One"); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, a, "playing the resolved stream", func() bool { return a.player.Playing() && a.player.Position() > 0 })
	if !r.asked("vid-3") {
		t.Fatal("the track did not come from the stream resolver")
	}
	position := a.player.Position()
	tt = downloadFolderSettings(t, a)
	if err := tt.Click("Choose download folder"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(filepath.Join(selected, "Meiro Downloads")) || !a.player.Playing() || a.player.Position() < position {
		t.Errorf("the folder should change without stopping the streamed song: %q", tt.Texts())
	}
}

// An album downloaded from its page opens, lists and plays with no network,
// and removing the download deletes its audio.
func TestADownloadedAlbumPlaysOffline(t *testing.T) {
	mediaURL := downloadTone(t)
	a, r := playbackApp(t)
	dir := filepath.Join(t.TempDir(), "Downloads")
	a.openDownloads(dir)
	r.url = func(string, int) (string, error) { return mediaURL, nil }
	a.router.Push("/album/MPREb_test")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.Click("Download"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	// The index is written off the main thread, and a write under way keeps
	// its own temporary file in the folder, so wait for it before counting.
	a.downloads.writer.Wait()
	if !tt.HasText("Downloaded") || len(audioFiles(t, dir)) != 1 {
		t.Fatalf("the album did not download: files=%q texts=%q", audioFiles(t, dir), tt.Texts())
	}
	// The Downloads page lists the album, and leaves the album's own page as
	// it was.
	if err := tt.Click("Downloads"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.router.Back()
	tt.Frame()
	if a.path() != "/album/MPREb_test" || tt.HasText("Albums and playlists") || !tt.HasText("Album Track One") {
		t.Fatalf("the album page took the Downloads page's content: path=%q texts=%q", a.path(), tt.Texts())
	}

	// The network goes away, and the app starts again from what it saved.
	a.player.Stop()
	a, r = playbackApp(t)
	a.openDownloads(dir)
	r.url = func(string, int) (string, error) { return "", errors.New("offline") }
	a.account.public = youtube.NewClient(youtube.Options{APIKey: "test", HTTPClient: &http.Client{Transport: unreachable{}}})
	a.router.Push("/downloads")
	tt = ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if !tt.HasText("Deep Focus") || !tt.HasText("Album Track One") {
		t.Fatalf("the Downloads page should list the album and its song: %q", tt.Texts())
	}
	if err := tt.Click("Deep Focus"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.path() != "/album/MPREb_test" || !tt.HasText("Album Track One") {
		t.Fatalf("the album should open from its download: path=%q texts=%q", a.path(), tt.Texts())
	}
	if err := tt.Click("Album Track One"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.current.VideoID != "vid-3" || a.playErr != "" || r.asked("vid-3") {
		t.Fatalf("the song should play from its download: current=%q err=%q resolved=%v", a.current.VideoID, a.playErr, r.asked("vid-3"))
	}

	a.player.Stop()
	if err := tt.Click("Downloaded"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.downloads.writer.Wait()
	if files := audioFiles(t, dir); len(files) != 0 || !tt.HasText("Download") {
		t.Fatalf("removing the download should delete its audio: files=%q texts=%q", files, tt.Texts())
	}
}

// A playlist downloaded once picks up a song added to it afterwards, the next
// time its page opens, and holds it offline with the rest.
func TestADownloadedPlaylistPicksUpASongAddedLater(t *testing.T) {
	mediaURL := downloadTone(t)
	fakePlaylistGrows.Store(false)
	t.Cleanup(func() { fakePlaylistGrows.Store(false) })

	a, r := playbackApp(t)
	dir := filepath.Join(t.TempDir(), "Downloads")
	a.openDownloads(dir)
	r.url = func(string, int) (string, error) { return mediaURL, nil }

	a.router.Push("/playlist/VLPL_grows")
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	// Opening a playlist that was never downloaded saves nothing.
	if files := audioFiles(t, dir); len(files) != 0 {
		t.Fatalf("opening an undownloaded playlist saved %q", files)
	}
	if err := tt.Click("Download"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	a.downloads.writer.Wait()
	if !tt.HasText("Downloaded") || len(audioFiles(t, dir)) != 1 {
		t.Fatalf("the playlist did not download: files=%q texts=%q", audioFiles(t, dir), tt.Texts())
	}

	// A song joins the playlist elsewhere, and the page opens again.
	fakePlaylistGrows.Store(true)
	if err := tt.Click("Downloads"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.pages.Clear()
	if err := tt.Click("Growing Playlist"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	a.downloads.writer.Wait()
	if !tt.HasText("Growing Track Two") || !tt.HasText("Downloaded") {
		t.Fatalf("the new song was not downloaded: %q", tt.Texts())
	}
	if files := audioFiles(t, dir); len(files) != 2 {
		t.Fatalf("files=%q, want the playlist's two songs", files)
	}
	collection, ok := a.downloads.lib.Collection("VLPL_grows")
	if !ok || !slices.Equal(collection.Tracks, []string{"grow-one", "grow-two"}) {
		t.Fatalf("collection=%#v, want both songs in the playlist's order", collection)
	}
	if n := r.count("grow-one"); n != 1 {
		t.Errorf("the song downloaded before was fetched %d times, want once", n)
	}

	// Offline, the playlist's page holds both songs.
	page, ok := a.offlinePage("VLPL_grows")
	if !ok {
		t.Fatal("the playlist has no offline page")
	}
	var titles []string
	for _, item := range page.Items {
		titles = append(titles, item.Title)
	}
	if !slices.Equal(titles, []string{"Growing Track One", "Growing Track Two"}) {
		t.Fatalf("the offline page holds %q, want both songs", titles)
	}
}
