package offline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Move copies lib to a new folder and publishes it with commit. It retires
// the source files only after commit has saved the new location.
func Move(ctx context.Context, from, to string, lib Library, commit func() error) error {
	resolved, err := filepath.EvalSymlinks(from)
	if err != nil {
		return fmt.Errorf("open current download folder: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(to))
	if err != nil {
		return fmt.Errorf("open destination parent folder: %w", err)
	}
	to = filepath.Join(parent, filepath.Base(to))
	if rel, err := filepath.Rel(resolved, to); err == nil && filepath.IsLocal(rel) {
		return errors.New("the destination is inside the current download folder")
	}
	source, err := os.OpenRoot(resolved)
	if err != nil {
		return fmt.Errorf("open current download folder: %w", err)
	}
	defer source.Close()
	if err := os.Mkdir(to, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("the destination already exists; choose a different folder")
		}
		return fmt.Errorf("create destination folder: %w", err)
	}
	target, err := os.OpenRoot(to)
	if err != nil {
		return fmt.Errorf("open destination folder: %w", err)
	}
	var copied []string
	committed := false
	defer func() {
		if !committed {
			retire(target, to, copied)
		}
		_ = target.Close()
	}()

	for _, song := range lib.Songs {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := song.File
		if !managedFile(song) {
			return fmt.Errorf("unexpected file name for %q", song.Item.Title)
		}
		if err := copyTrack(source, target, name); err != nil {
			return fmt.Errorf("copy download %q: %w", song.Item.Title, err)
		}
		copied = append(copied, name)
	}
	index, err := encode(lib)
	if err != nil {
		return fmt.Errorf("encode download index: %w", err)
	}
	if err := writeCopy(target, indexName, bytes.NewReader(index), int64(len(index))); err != nil {
		return fmt.Errorf("copy download index: %w", err)
	}
	copied = append(copied, indexName)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !sameDirectory(target, to) {
		return errors.New("the destination drive is no longer available")
	}
	if err := commit(); err != nil {
		return fmt.Errorf("save download folder: %w", err)
	}
	committed = true
	retire(source, resolved, copied)
	return nil
}

func copyTrack(source, target *os.Root, name string) error {
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
	return writeCopy(target, name, in, info.Size())
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

func sameDirectory(root *os.Root, path string) bool {
	opened, err := root.Stat(".")
	if err != nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(opened, current)
}

func retire(root *os.Root, path string, names []string) {
	for _, name := range names {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("cleaning up moved download %s: %v", name, err)
		}
	}
	if sameDirectory(root, path) {
		_ = os.Remove(path) // Only removes an empty directory.
	}
}
