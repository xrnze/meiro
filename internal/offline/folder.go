package offline

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/elianiva/meiro/internal/audio"
)

const (
	identityName = ".meiro-download-library"
	audioTemp    = ".meiro-audio-"
	indexTemp    = ".meiro-index-"
)

// ErrUnavailable reports that the configured drive or library is not present.
var ErrUnavailable = errors.New("download folder unavailable")

// Folder identifies the library at Dir. ID is empty for the default folder.
// A chosen drive must match its ID before Meiro can read or modify it.
type Folder struct {
	Dir string `json:"dir"`
	ID  string `json:"id"`
}

func (f Folder) openRoot(ctx context.Context) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(f.Dir)
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	if f.ID != "" {
		id, err := readRegular(root, identityName)
		if err != nil || string(id) != f.ID {
			_ = root.Close()
			return nil, ErrUnavailable
		}
	}
	return root, nil
}

// Available reports whether the chosen library is present at the configured path.
func (f Folder) Available(ctx context.Context) bool {
	root, err := f.openRoot(ctx)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Lstat(indexName)
	return err == nil && info.Mode().IsRegular()
}

// Open reads and validates the library at f.
func (f Folder) Open(ctx context.Context) (Library, error) {
	root, err := f.openRoot(ctx)
	if err != nil {
		return Library{}, err
	}
	defer root.Close()
	data, err := readRegular(root, indexName)
	if err != nil {
		return Library{}, err
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, err
	}
	for _, song := range lib.Songs {
		if !managedFile(song) {
			return Library{}, fmt.Errorf("unexpected file name for %q", song.Item.Title)
		}
	}
	lib.reindex()
	sweepTemps(root)
	return lib, nil
}

func readRegular(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return root.ReadFile(name)
}

func sweepTemps(root *os.Root) {
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	defer dir.Close()
	entries, _ := dir.ReadDir(-1)
	for _, entry := range entries {
		name := entry.Name()
		key, ok := strings.CutPrefix(name, audioTemp)
		if !ok {
			key, ok = strings.CutPrefix(name, indexTemp)
		}
		if entry.Type().IsRegular() && ok && len(key) >= 26 {
			if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(key); err == nil {
				if err := root.Remove(name); err != nil {
					log.Printf("removing temporary download file %s: %v", name, err)
				}
			}
		}
	}
}

// Write atomically replaces the index of the validated library.
func (f Folder) Write(ctx context.Context, lib Library) error {
	root, err := f.openRoot(ctx)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := encode(lib)
	if err != nil {
		return err
	}
	name := indexTemp + rand.Text()
	if err := writeCopy(root, name, bytes.NewReader(data), int64(len(data))); err != nil {
		return err
	}
	defer func() { _ = root.Remove(name) }()
	return root.Rename(name, indexName)
}

// Download saves a full track in the validated library using a pinned directory.
func (f Folder) Download(ctx context.Context, videoID, streamURL string) (string, error) {
	root, err := f.openRoot(ctx)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := audioTemp + rand.Text()
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Remove(name) }()
	ext, err := audio.Fetch(ctx, streamURL, out)
	if err == nil {
		err = out.Sync()
	}
	if err := errors.Join(err, out.Close(), ctx.Err()); err != nil {
		return "", err
	}
	file := audioKey(videoID) + ext
	if err := root.Rename(name, file); err != nil {
		return "", err
	}
	return file, nil
}

// Remove removes only the named files in the validated library.
func (f Folder) Remove(ctx context.Context, files []string) error {
	root, err := f.openRoot(ctx)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range files {
		if !filepath.IsLocal(name) || filepath.Base(name) != name {
			return fmt.Errorf("unsafe download file name %q", name)
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
