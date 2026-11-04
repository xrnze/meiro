package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/playstate"
	"github.com/elianiva/meiro/youtube"
)

// playerbarApp is an app whose current track is already set, so the player
// bar shows with its artist and album links, without any playback or network.
func playerbarApp(t *testing.T) *app {
	t.Helper()
	a := newTestApp(t)
	track := youtube.MusicItem{
		VideoID:  "a",
		Title:    "Bar Track",
		Subtitle: "Aurora Vale • Deep Focus",
		Byline: &youtube.TrackByline{Parts: []youtube.BylinePart{
			{Text: "Aurora Vale", BrowseID: "UCartist"},
			{Text: " • "},
			{Text: "Deep Focus", BrowseID: "MPREb_test"},
		}},
	}
	a.queue.restore(playstate.State{Items: []youtube.MusicItem{track}, Index: 0, RecStart: -1})
	a.current, a.total, a.resumeID = track, 60*time.Second, track.VideoID
	return a
}

func TestPlayerBarBackgroundOpensTheFullPlayer(t *testing.T) {
	a := playerbarApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	// The whole bar is one background press: clicking its top-left padding,
	// where no content sits, opens the full player. (Pointer-reachable, and
	// with Enter and Space on the focused bar, so keyboard users get it too.)
	bar, ok := tt.Find("Open the full player")
	if !ok {
		t.Fatal("the player bar has no labeled background press")
	}
	tt.ClickAt(bar.X+20, bar.Y+4)
	tt.Frame()
	if !a.nowPlaying.open || a.nowPlaying.closing {
		t.Fatalf("pressing the bar background did not open the player: open=%v closing=%v", a.nowPlaying.open, a.nowPlaying.closing)
	}
	if err := tt.Click("Close the player"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.nowPlaying.open {
		t.Fatal("the player did not close")
	}
	tabTo(t, tt, "Open the full player")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !a.nowPlaying.open || a.nowPlaying.closing {
		t.Fatal("Enter on the focused bar background did not open the player")
	}
	if err := tt.Click("Close the player"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.nowPlaying.open {
		t.Fatal("the player did not close again")
	}
	// The right padding strip is background too.
	tt.ClickAt(bar.X+bar.W-24, bar.Y+4)
	tt.Frame()
	if !a.nowPlaying.open || a.nowPlaying.closing {
		t.Fatal("pressing the bar's right padding did not open the player")
	}
}

func TestPlayerBarInfoIsNoLongerAButton(t *testing.T) {
	a := playerbarApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	// The track info has no button of its own: the only control that still
	// says "Open the player" is the chevron, a small icon at the bar's end,
	// not a wide press target over the track's name and cover.
	chevron, ok := tt.Find("Open the player")
	if !ok {
		t.Fatal(`no "Open the player" control remains`)
	}
	if chevron.W > 80 {
		t.Fatalf(`"Open the player" is still the wide info button: %v`, chevron)
	}
	tt.ClickAt(chevron.X+chevron.W/2, chevron.Y+chevron.H/2)
	tt.Frame()
	if !a.nowPlaying.open || a.nowPlaying.closing {
		t.Fatal("the chevron no longer opens the player")
	}
	// Its press belongs to the bar background now: pressing the track's name
	// opens the player as pressing any empty area of the bar does.
	if err := tt.Click("Close the player"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if a.nowPlaying.open {
		t.Fatal("the player did not close")
	}
	title, ok := tt.Find("Bar Track")
	if !ok {
		t.Fatal("the current track's name is missing")
	}
	tt.ClickAt(title.X+title.W/2, title.Y+title.H/2)
	tt.Frame()
	if !a.nowPlaying.open || a.nowPlaying.closing {
		t.Fatal("pressing the track name did not open the player as the bar background")
	}
}

func TestPlayerBarScrubberSeeksAndStaysInTheBar(t *testing.T) {
	a := playerbarApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	slider, ok := tt.Find("Position")
	if !ok {
		t.Fatal("the player bar scrubber is missing")
	}
	x, y := slider.X+slider.W/2, slider.Y+slider.H/2
	tt.Press(x, y)
	tt.Frame()
	if a.nowPlaying.open {
		t.Fatal("pressing the scrubber opened the player")
	}
	tt.Release(x, y)
	tt.Frame()
	if a.nowPlaying.open {
		t.Fatal("pressing the scrubber opened the player")
	}
	if a.seekVersion == 0 || a.playbackPosition() <= 0 {
		t.Fatalf("the scrubber press did not seek: seek=%d position=%v", a.seekVersion, a.playbackPosition())
	}
}

func TestPlayerBarInfoLinksStillNavigate(t *testing.T) {
	a := playerbarApp(t)
	tt := ui.NewTester(a.view, 1440, 900)
	for _, dest := range []struct{ label, path string }{
		{"Go to artist: Aurora Vale", "/artist/UCartist"},
		{"Go to album: Deep Focus", "/album/MPREb_test"},
	} {
		if err := tt.Click(dest.label); err != nil {
			t.Fatal(err)
		}
		tt.Advance(time.Second)
		if a.nowPlaying.open {
			t.Fatalf("%s opened the player", dest.label)
		}
		if a.router.Path() != dest.path {
			t.Fatalf("%s did not navigate: path %q want %q", dest.label, a.router.Path(), dest.path)
		}
		if err := tt.Click("Back"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(time.Second)
		if a.router.Path() != "/home" {
			t.Fatalf("Back did not return home: path %q", a.router.Path())
		}
	}
}
