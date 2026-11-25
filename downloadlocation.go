package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/elianiva/meiro/internal/fsutil"
	"github.com/elianiva/meiro/internal/offline"
	"github.com/elianiva/meiro/m3"
)

const downloadFolderUnavailable = "Download folder is unavailable. Reconnect the drive to use your downloads."

var errDownloadFolderUnavailable = errors.New("download folder unavailable")

// The location is committed independently of the debounced settings writer so
// a failed preference write cannot leave the old library deleted.
func (a *app) setupDownloadLocation(data string) {
	d := &a.downloads
	d.defaultDir = filepath.Join(data, "Downloads")
	d.locationFile = filepath.Join(data, "download-location")
	location, err := os.ReadFile(d.locationFile)
	switch {
	case errors.Is(err, os.ErrNotExist), err == nil && len(location) == 0:
		a.openDownloads(d.defaultDir)
	case err == nil && filepath.IsAbs(string(location)):
		d.dir = filepath.Clean(string(location))
	default:
		d.locationErr = "Could not read the saved download location."
		log.Printf("reading download location: %v", err)
	}
}

func (d *downloads) custom() bool { return d.locationFile != "" && d.dir != d.defaultDir }

func (d *downloads) unavailableMessage() string {
	if d.locationErr != "" {
		return d.locationErr
	}
	return downloadFolderUnavailable
}

func (d *downloads) setAvailable(available bool) {
	if d.available != available {
		d.available = available
		d.version++
	}
}

func (a *app) checkDownloadFolder() {
	d := &a.downloads
	if !d.custom() || d.dir == "" || d.move.busy() || d.probe.busy() {
		return
	}
	dir, wasAvailable := d.dir, d.available
	if !wasAvailable && (d.files.Load() != 0 || d.job.busy()) {
		return
	}
	type availability struct {
		lib  offline.Library
		open bool
	}
	startTask(a, &d.probe, noTimeout, func(ctx context.Context) (availability, error) {
		if wasAvailable {
			return availability{open: offline.Available(ctx, dir)}, nil
		}
		d.writer.Wait()
		lib, err := offline.Open(ctx, dir)
		return availability{lib: lib, open: err == nil}, err
	}, func(result availability, _ error) {
		if d.available != wasAvailable {
			return
		}
		if result.open && !wasAvailable {
			d.lib = result.lib
		}
		d.setAvailable(result.open)
		a.nextDownload()
	})
}

func (a *app) downloadFolderBlock() string {
	d := &a.downloads
	switch {
	case d.move.busy():
		return "Moving downloads..."
	case !d.available:
		return d.unavailableMessage()
	case d.job.busy() || len(d.queue) != 0 || d.files.Load() != 0 || !a.player.CanMoveFilesIn(d.dir):
		return "Wait for downloads or offline playback to finish before moving the folder."
	}
	return ""
}

func (a *app) downloadFolderCard(c *ui.Context) {
	d := &a.downloads
	a.settingsCard(c, "Download folder", m3.IconFolder, func() {
		ui.Column(c).Gap(12).Children(func() {
			settingLabel(c, "Offline storage", "Meiro moves your saved songs, albums and playlists into a Meiro Downloads folder inside the location you choose.")
			sc := m3.Of(c).Scheme
			m3.Text(c, m3.BodyMedium, d.dir).TextColor(sc.OnSurfaceVariant)
			block := a.downloadFolderBlock()
			disabled := block != "" || d.choosing || d.locationFile == ""
			ui.Row(c).Gap(8).Children(func() {
				if m3.Button(c, m3.ButtonSpec{Label: "Choose download folder", Icon: m3.IconFolder, Kind: m3.Outlined, Size: m3.Small40, Disabled: disabled, Key: "choose-download-folder"}).Clicked() {
					a.chooseDownloadFolder()
				}
				if d.custom() && d.dir != "" && m3.Button(c, m3.ButtonSpec{Label: "Reset download folder", Kind: m3.TextOnly, Size: m3.Small40, Disabled: disabled, Key: "reset-download-folder"}).Clicked() {
					a.moveDownloadFolder(d.defaultDir)
				}
			})
			if block != "" {
				m3.Text(c, m3.BodyMedium, block).TextColor(sc.OnSurfaceVariant)
			} else if d.locationErr != "" {
				m3.Text(c, m3.BodyMedium, d.locationErr).TextColor(sc.Error)
			}
		})
	})
}

func (a *app) chooseDownloadFolder() {
	d := &a.downloads
	if d.choosing || a.downloadFolderBlock() != "" || d.locationFile == "" {
		return
	}
	d.choosing = true
	parent, current := a.desktop.window(), filepath.Dir(d.dir)
	pick := d.pickFolder
	if pick == nil {
		pick = func(current string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
				Parent: parent, Title: "Choose download folder", DefaultPath: current,
				Directory: true, CreateDirectories: true,
			})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		}
	}
	type choice struct {
		parent string
		err    error
	}
	spawn(a, func() choice {
		parent, err := pick(current)
		if err == nil && parent != "" {
			parent, err = filepath.EvalSymlinks(parent)
		}
		return choice{parent: parent, err: err}
	}, func(result choice) {
		d.choosing = false
		if result.err != nil {
			d.locationErr = "Could not choose a download folder: " + result.err.Error()
		} else if result.parent != "" {
			a.moveDownloadFolder(filepath.Join(result.parent, "Meiro Downloads"))
		}
	})
}

func (a *app) moveDownloadFolder(to string) {
	d := &a.downloads
	if to == d.dir || d.locationFile == "" || a.downloadFolderBlock() != "" {
		return
	}
	d.probe.stop()
	d.locationErr = ""
	from, lib, file, custom := d.dir, d.lib, d.locationFile, d.custom()
	location := to
	if to == d.defaultDir {
		location = ""
	}
	startTask(a, &d.move, noTimeout, func(ctx context.Context) (struct{}, error) {
		d.writer.Wait()
		if custom && !offline.Available(ctx, from) {
			return struct{}{}, errDownloadFolderUnavailable
		}
		err := offline.Move(ctx, from, to, lib, func() error {
			return fsutil.WriteFileAtomicInDir(file, []byte(location), 0o600)
		})
		return struct{}{}, err
	}, func(_ struct{}, err error) {
		if errors.Is(err, errDownloadFolderUnavailable) {
			d.setAvailable(false)
			return
		}
		if err != nil {
			d.locationErr = "Could not move downloads: " + err.Error()
			return
		}
		d.dir = to
		d.version++
	})
}
