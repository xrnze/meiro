package main

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Shuffle hides what plays next: every row ahead of the track playing is
// blurred, so neither a name nor a cover gives the next track away, and a row
// opens up while the pointer rests on it. The track playing stays sharp.
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

	// sharpness is how much neighbouring pixels of a row differ: sharp text
	// and a sharp cover edge change from pixel to pixel, blurred ones barely
	// move. It reads a row's own box, so a row that moved down the list is
	// measured the same way.
	sharpness := func(label string) float64 {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("the queue shows no row %q: %q", label, tt.Texts())
		}
		img := tt.Image()
		total, count := 0.0, 0
		lum := func(x, y int) float64 {
			p := img.RGBAAt(x, y)
			return 0.2126*float64(p.R) + 0.7152*float64(p.G) + 0.0722*float64(p.B)
		}
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W)-1; x++ {
				total += math.Abs(lum(x+1, y) - lum(x, y))
				count++
			}
		}
		return total / float64(count)
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
	open := sharpness("Track b")
	if open < next/2 {
		t.Errorf("the row under the pointer stayed blurred: sharpness %.2f against %.2f", open, next)
	}
	tt.Move(1, 1)
	settle()
	if again := sharpness("Track b"); again > next/2 {
		t.Errorf("the row stayed open after the pointer left: sharpness %.2f against %.2f", again, next)
	}
	// The list's own cursor opens a row too, and so does the row's menu: the
	// keyboard reaches both without the pointer.
	tabTo(t, tt, "Track a")
	tt.Key(0, ui.KeyHome)
	tt.Key(0, ui.KeyDown)
	settle()
	index, ok := a.queue.trackAt(a.nowPlaying.queueSelectionCursor)
	if !ok || index <= a.queue.index {
		t.Fatalf("the cursor is on track %d, not on one to come", index)
	}
	picked := a.queue.at(index).Title
	if got := sharpness(picked); got < next/2 {
		t.Errorf("the row the cursor is on stayed blurred: sharpness %.2f against %.2f", got, next)
	}
	tt.Key(ui.Shift, ui.KeyF10)
	settle()
	if got := sharpness(picked); got < next/2 {
		t.Errorf("the row whose menu is open stayed blurred: sharpness %.2f against %.2f", got, next)
	}
	tt.Key(0, ui.KeyEscape)
	settle()

	// Turning shuffle off shows the list again.
	a.setShuffle(false)
	settle()
	if shown := sharpness("Track b"); shown < next/2 {
		t.Errorf("turning shuffle off left the queue blurred: sharpness %.2f against %.2f", shown, next)
	}
}
