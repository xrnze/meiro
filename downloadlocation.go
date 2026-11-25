package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
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

// The location is committed independently of the debounced settings writer so
// a failed preference write cannot leave the old library deleted.
func (a *app) setupDownloadLocation(data string) {
	d := &a.downloads
	d.defaultDir = filepath.Join(data, "Downloads")
	d.locationFile = filepath.Join(data, "download-location")
	location, err := os.ReadFile(d.locationFile)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(location) == 0 {
		a.openDownloads(d.defaultDir)
		return
	}
	var folder offline.Folder
	if err == nil {
		err = json.Unmarshal(location, &folder)
	}
	if err != nil || !filepath.IsAbs(folder.Dir) || folder.ID == "" {
		d.locationErr = "Could not read the saved download location."
		log.Printf("reading download location: %v", err)
		return
	}
	d.dir, d.locationID = filepath.Clean(folder.Dir), folder.ID
}

func (d *downloads) custom() bool { return d.locationID != "" }

func (d *downloads) folder() offline.Folder { return offline.Folder{Dir: d.dir, ID: d.locationID} }

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
	folder, wasAvailable := d.folder(), d.available
	if !wasAvailable && (d.files.Load() != 0 || d.job.busy()) {
		return
	}
	type availability struct {
		lib  offline.Library
		open bool
	}
	startTask(a, &d.probe, noTimeout, func(ctx context.Context) (availability, error) {
		if wasAvailable {
			return availability{open: folder.Available(ctx)}, nil
		}
		d.writer.Wait()
		lib, err := folder.Open(ctx)
		return availability{lib: lib, open: err == nil}, err
	}, func(result availability, _ error) {
		if d.available != wasAvailable {
			return
		}
		if result.open && !wasAvailable {
			if d.loaded {
				// The verified drive may have missed a pending index write.
				d.writer.Save(folder, d.lib)
			} else {
				d.lib = result.lib
				d.loaded = true
			}
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
	if to == d.dir || d.locationFile == "" {
		return
	}
	if block := a.downloadFolderBlock(); block != "" {
		a.notice = "Download folder was not changed. " + block
		return
	}
	d.probe.stop()
	d.locationErr = ""
	from, lib, file := d.folder(), d.lib, d.locationFile
	target := offline.Folder{Dir: to}
	if to != d.defaultDir {
		target.ID = rand.Text()
	}
	done := make(chan struct{})
	d.moveDone = done
	startTask(a, &d.move, noTimeout, func(ctx context.Context) (bool, error) {
		defer close(done)
		d.writer.Wait()
		return offline.Move(ctx, from, target, lib, func() (bool, error) {
			var data []byte
			if target.ID != "" {
				var err error
				data, err = json.Marshal(target)
				if err != nil {
					return false, err
				}
			}
			if err := fsutil.WriteFileAtomicInDir(file, data, 0o600); err != nil {
				return false, err
			}
			return fsutil.SyncDir(ctx, filepath.Dir(file)) == nil, nil
		})
	}, func(kept bool, err error) {
		d.moveDone = nil
		if errors.Is(err, offline.ErrUnavailable) && d.custom() {
			d.setAvailable(false)
			return
		}
		if err != nil {
			d.locationErr = "Could not move downloads: " + err.Error()
			return
		}
		d.dir, d.locationID = to, target.ID
		d.version++
		if kept {
			a.notice = "Downloads moved. Meiro kept the old copy because it could not confirm the move was safely written to disk."
		}
	})
}
