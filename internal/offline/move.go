package offline

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/elianiva/meiro/internal/fsutil"
)

// Move copies lib to to, then calls commit to publish the new location and
// record any default library left behind. kept reports unconfirmed durability.
func Move(ctx context.Context, from, to Folder, lib Library, previousDefault string, commit func(string) (bool, error)) (kept bool, err error) {
	resolved, err := filepath.EvalSymlinks(from.Dir)
	if err != nil {
		return false, fmt.Errorf("open current download folder: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(to.Dir))
	if err != nil {
		return false, fmt.Errorf("open destination parent folder: %w", err)
	}
	to.Dir = filepath.Join(parent, filepath.Base(to.Dir))
	if rel, err := filepath.Rel(resolved, to.Dir); err == nil && filepath.IsLocal(rel) {
		return false, errors.New("the destination is inside the current download folder")
	}
	source, err := from.openRoot(ctx)
	if err != nil {
		return false, fmt.Errorf("open current download folder: %w", err)
	}
	defer source.Close()
	defaultIndex := previousDefault
	if from.ID == "" {
		data, err := readRegular(source, indexName)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("read current download index: %w", err)
		}
		defaultIndex = ""
		if err == nil {
			defaultIndex = indexDigest(data)
		}
	}
	mkdirErr := os.Mkdir(to.Dir, 0o700)
	created := mkdirErr == nil
	if !created && (!errors.Is(mkdirErr, os.ErrExist) || to.ID != "") {
		if errors.Is(mkdirErr, os.ErrExist) {
			return false, errors.New("the destination already exists; choose a different folder")
		}
		return false, fmt.Errorf("create destination folder: %w", mkdirErr)
	}
	target, err := os.OpenRoot(to.Dir)
	if err != nil {
		return false, fmt.Errorf("open destination folder: %w", err)
	}
	var copied []string
	committed := false
	defer func() {
		if !committed {
			retire(target, to.Dir, copied, created)
		} else {
			_ = target.Close()
		}
	}()
	if !sameDirectory(target, to.Dir) {
		return false, errors.New("the destination is not a regular directory")
	}
	if !created {
		if err := archiveDefault(ctx, target, previousDefault); err != nil {
			return false, err
		}
	}

	for _, song := range lib.Songs {
		if !managedFile(song) {
			return false, fmt.Errorf("unexpected file name for %q", song.Item.Title)
		}
		if err := copyTrack(ctx, source, target, song.File); err != nil {
			return false, fmt.Errorf("copy download %q: %w", song.Item.Title, err)
		}
		copied = append(copied, song.File)
	}
	index, err := encode(lib)
	if err != nil {
		return false, fmt.Errorf("encode download index: %w", err)
	}
	if err := writeCopy(target, indexName, bytes.NewReader(index), int64(len(index))); err != nil {
		return false, fmt.Errorf("copy download index: %w", err)
	}
	copied = append(copied, indexName)
	retired := append([]string(nil), copied...)
	if from.ID != "" {
		retired = append(retired, identityName)
	}
	if to.ID != "" {
		if err := writeCopy(target, identityName, strings.NewReader(to.ID), int64(len(to.ID))); err != nil {
			return false, fmt.Errorf("identify download folder: %w", err)
		}
		copied = append(copied, identityName)
	}
	syncErr := errors.Join(syncRoot(target), fsutil.SyncDir(ctx, parent))
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !sameDirectory(target, to.Dir) {
		return false, errors.New("the destination drive is no longer available")
	}
	durable, err := commit(defaultIndex)
	if err != nil {
		return false, fmt.Errorf("save download folder: %w", err)
	}
	committed = true
	if syncErr != nil || !durable {
		log.Printf("moved downloads: keeping old copy because durability was not confirmed: %v", syncErr)
		return true, nil
	}
	retire(source, resolved, retired, true)
	return false, nil
}

func indexDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// archiveDefault separates a previously retained copy from the new default.
// The selected library is still on the other drive, even if archiving fails.
func archiveDefault(ctx context.Context, target *os.Root, expected string) (err error) {
	data, err := readRegular(target, indexName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || expected == "" || indexDigest(data) != expected {
		return errors.New("the default folder already contains a different download index")
	}
	lib, err := decodeLibrary(data)
	if err != nil {
		return fmt.Errorf("read retained download library: %w", err)
	}
	backup := "Meiro Downloads backup " + rand.Text()
	if err := target.Mkdir(backup, 0o700); err != nil {
		return fmt.Errorf("create download backup: %w", err)
	}
	var archived []string
	defer func() {
		if err == nil {
			return
		}
		for _, name := range archived {
			if _, statErr := target.Lstat(name); errors.Is(statErr, os.ErrNotExist) {
				err = errors.Join(err, target.Rename(filepath.Join(backup, name), name))
			}
		}
		_ = target.Remove(backup)
	}()
	for _, song := range lib.Songs {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := target.Lstat(song.File)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("cannot archive download %q", song.Item.Title)
		}
		if err := target.Rename(song.File, filepath.Join(backup, song.File)); err != nil {
			return fmt.Errorf("archive download %q: %w", song.Item.Title, err)
		}
		archived = append(archived, song.File)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := target.Rename(indexName, filepath.Join(backup, indexName)); err != nil {
		return fmt.Errorf("archive download index: %w", err)
	}
	if dir, err := target.OpenRoot(backup); err == nil {
		if err := syncRoot(dir); err != nil {
			log.Printf("syncing download backup: %v", err)
		}
		_ = dir.Close()
	}
	return nil
}

func copyTrack(ctx context.Context, source, target *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := source.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular audio file")
	}
	in, err := source.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	actual, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, actual) {
		return errors.New("audio file changed while opening it")
	}
	return writeCopy(target, name, checkedReader{ctx, in}, info.Size())
}

type checkedReader struct {
	ctx context.Context
	r   io.Reader
}

func (r checkedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func writeCopy(target *os.Root, name string, data io.Reader, size int64) (err error) {
	out, err := target.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, out.Close())
		if err != nil {
			_ = target.Remove(name)
		}
	}()
	n, err := io.Copy(out, data)
	if err != nil {
		return err
	}
	if n != size {
		return io.ErrUnexpectedEOF
	}
	return out.Sync()
}

func syncRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func sameDirectory(root *os.Root, path string) bool {
	opened, err := root.Stat(".")
	if err != nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(opened, current)
}

func retire(root *os.Root, path string, names []string, removeDir bool) {
	for _, name := range names {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("cleaning up moved download %s: %v", name, err)
		}
	}
	opened, err := root.Stat(".")
	_ = root.Close()
	if removeDir && err == nil {
		current, err := os.Lstat(path)
		if err == nil && os.SameFile(opened, current) {
			_ = os.Remove(path) // Only removes an empty directory.
		}
	}
}
