package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Shuffle hides what plays next: every row ahead of the track playing is
// blurred, so neither a name nor a cover gives the next track away, and a row
// opens up while the listener is on it. The track playing stays sharp.
func TestShuffleBlursTheRowsItHides(t *testing.T) {
	a, _ := playbackApp(t)
	q := tracks("a", "b", "c", "d")
	a.current = q[0]
	a.queue.replace(q[0], q, 0, "Shuffle test")
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	// The first frame navigates, which closes a player already open.
	a.nowPlaying.open, a.nowPlaying.tab = true, tabQueue
	// The player rises and the list scrolls the track playing to the top: a
	// frame still in motion has the rows where they were a frame ago, so
	// settle and read a row's box only once they are drawn where they are.
	settle := func() {
		for range 2 {
			tt.Advance(time.Second)
			tt.Frame()
		}
	}
	settle()

	// roughness is how much neighbouring pixels of a box differ: sharp text
	// and a sharp cover edge change from pixel to pixel, blurred ones barely
	// move. A row that moved down the list is measured the same way.
	roughness := func(r ui.Rect) float64 {
		img := tt.Image()
		lum := func(x, y int) float64 {
			p := img.RGBAAt(x, y)
			return 0.2126*float64(p.R) + 0.7152*float64(p.G) + 0.0722*float64(p.B)
		}
		total, count := 0.0, 0
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W)-1; x++ {
				total += math.Abs(lum(x+1, y) - lum(x, y))
				count++
			}
		}
		return total / float64(count)
	}
	// sharpness reads the middle of a row, where its name is, clear of the
	// outline the list draws around the edges of the row its cursor is on:
	// artwork and the row's padding on the left, the length on the right.
	sharpness := func(label string) float64 {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("the queue shows no row %q: %q", label, tt.Texts())
		}
		return roughness(ui.Rect{X: r.X + 68, Y: r.Y + 8, W: r.W - 136, H: r.H - 16})
	}

	next, played := sharpness("Track b"), sharpness("Track a")
	a.setShuffle(true)
	settle()
	blurred := sharpness("Track b")
	if blurred > next/2 {
		t.Errorf("shuffle left the tracks to come readable: sharpness %.2f against %.2f", blurred, next)
	}
	if now := sharpness("Track a"); now < played/2 {
		t.Errorf("shuffle blurred the track playing: sharpness %.2f against %.2f", now, played)
	}
	// The pointer opens the row it rests on, and it closes when it leaves.
	r, _ := tt.Find("Track b")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	settle()
	if open := sharpness("Track b"); open < next/2 {
		t.Errorf("the row under the pointer stayed blurred: sharpness %.2f against %.2f", open, next)
	}
	tt.Move(1, 1)
	settle()
	if again := sharpness("Track b"); again > next/2 {
		t.Errorf("the row stayed open after the pointer left: sharpness %.2f against %.2f", again, next)
	}

	// The keys open the row their cursor lands on, with the focus inside the
	// list but on another row's menu button.
	elsewhere := a.queue.at(2).Title
	tabTo(t, tt, "More options for "+elsewhere)
	tt.Key(0, ui.KeyHome)
	tt.Key(0, ui.KeyDown)
	settle()
	index, ok := a.queue.trackAt(a.nowPlaying.queueSelectionCursor)
	if !ok || index <= a.queue.index {
		t.Fatalf("the cursor is on track %d, not on one to come", index)
	}
	picked := a.queue.at(index).Title
	if picked == elsewhere {
		t.Fatalf("the cursor landed on %q, the row holding the focus", picked)
	}
	if got := sharpness(picked); got < next/2 {
		t.Errorf("the row the cursor is on stayed blurred: sharpness %.2f against %.2f", got, next)
	}
	// So does a row's own menu, which acts on that row.
	tt.Key(ui.Shift, ui.KeyF10)
	settle()
	if got := sharpness(picked); got < next/2 {
		t.Errorf("the row whose menu is open stayed blurred: sharpness %.2f against %.2f", got, next)
	}
	tt.Key(0, ui.KeyEscape)
	settle()

	// A chosen selection opens too, since its menu acts on all of it: the
	// rows the keys do not sit on are the ones a range needs to show.
	if err := tt.ClickWith(ui.Shift, picked); err != nil {
		t.Fatal(err)
	}
	other := ""
	for i := a.queue.index + 1; i < a.queue.len() && other == ""; i++ {
		if a.queue.at(i).Title != picked {
			other = a.queue.at(i).Title
		}
	}
	if other == "" {
		t.Fatal("the queue holds no other track to come")
	}
	if err := tt.ClickWith(ui.Shift, other); err != nil {
		t.Fatal(err)
	}
	tt.Move(1, 1)
	settle()
	if got := sharpness(picked); got < next/2 {
		t.Errorf("a chosen row stayed blurred: sharpness %.2f against %.2f", got, next)
	}

	// Turning shuffle off shows the list again.
	a.setShuffle(false)
	settle()
	if shown := sharpness("Track b"); shown < next/2 {
		t.Errorf("turning shuffle off left the queue blurred: sharpness %.2f against %.2f", shown, next)
	}
}
