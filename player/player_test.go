package player

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamArgsProtocolWhitelist(t *testing.T) {
	args := streamArgs("https://media.test/audio", 0, true, 1)
	index := slices.Index(args, "-protocol_whitelist")
	if index < 0 || index+1 == len(args) {
		t.Fatalf("no protocol whitelist in %v", args)
	}
	if slices.Contains(strings.Split(args[index+1], ","), "file") {
		t.Fatalf("remote stream may read local files: %q", args[index+1])
	}
}

func TestReapObservesExitWhileReaderIsPaused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses sh")
	}
	p := &Player{}
	cmd := exec.Command("sh", "-c", "exit 7")
	pipe, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	s := &session{cmd: cmd, pipe: pipe, done: make(chan struct{}), quit: make(chan struct{}), source: newCountingReader(pipe, nil)}
	go p.reap(s)
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("process exit was not observed while the reader was idle")
	}
	close(s.quit)
}

func TestCleanExitWithUnreadOutputIsNotAFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses sh")
	}
	p := &Player{}
	cmd := exec.Command("sh", "-c", "printf abcd")
	pipe, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	s := &session{
		cmd: cmd, pipe: pipe, stderr: &boundedBuffer{limit: 16},
		done: make(chan struct{}), quit: make(chan struct{}), source: newCountingReader(pipe, nil),
	}
	go p.reap(s)
	<-s.done
	if !p.pendingSamples(s) {
		t.Fatal("clean exit with unread output must not read as a failed stream")
	}
	data, err := io.ReadAll(s.source)
	if err != nil || string(data) != "abcd" {
		t.Fatalf("read %q, %v: output lost after exit", data, err)
	}
	if p.pendingSamples(s) {
		t.Fatal("nothing is pending once the reader has the data")
	}
}

// wakeDevice is an audio device that records what WakeDevice asks of it.
type wakeDevice struct {
	mu      sync.Mutex
	actions []string
	fail    error
}

func (d *wakeDevice) Err() error                   { return nil }
func (d *wakeDevice) newPlayer(r io.Reader) output { return &nullOutput{r: r} }

func (d *wakeDevice) record(what string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.actions = append(d.actions, what)
	return d.fail
}

func (d *wakeDevice) suspend() error { return d.record("suspend") }
func (d *wakeDevice) resume() error  { return d.record("resume") }

func (d *wakeDevice) did() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.actions)
}

func TestWakeDeviceFollowsNothingButChanges(t *testing.T) {
	dev := &wakeDevice{}
	audio := &sharedAudio{open: func() (device, error) { return dev, nil }}
	p := New(withAudio(audio))
	t.Cleanup(p.Stop)

	// Nothing is open, so there is nothing to suspend or resume.
	p.WakeDevice(false)
	if acted := dev.did(); len(acted) != 0 {
		t.Errorf("an unopened device was woken: %v", acted)
	}

	// Opening the device, as a first play does, leaves it awake.
	if _, err := audio.context(); err != nil {
		t.Fatal(err)
	}

	// Playback starting wakes the device, once, however often the app syncs.
	p.WakeDevice(true)
	p.WakeDevice(true)
	if acted := dev.did(); !slices.Equal(acted, []string{"resume"}) {
		t.Errorf("starting playback woke the device: %v", acted)
	}

	// Playback pausing or stopping suspends it, again once.
	p.WakeDevice(false)
	p.WakeDevice(false)
	if acted := dev.did(); !slices.Equal(acted, []string{"resume", "suspend"}) {
		t.Errorf("pausing playback parked the device: %v", acted)
	}

	// A failure is reported to the caller, never panics, and the next
	// change retries.
	boom := errors.New("boom")
	dev.mu.Lock()
	dev.fail = boom
	dev.mu.Unlock()
	p.WakeDevice(true)
	if acted := dev.did(); !slices.Equal(acted, []string{"resume", "suspend", "resume"}) {
		t.Errorf("a failing resume changed what the device saw: %v", acted)
	}
}
