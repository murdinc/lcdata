package lcdata

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchNodes watches the nodes directory (and all subdirectories) and
// hot-reloads the runner's node registry whenever a file is created,
// modified, removed, or renamed. New subdirectories are added to the
// watch set automatically. Events are debounced with a 200ms cooldown.
// The goroutine exits when ctx is cancelled.
func WatchNodes(ctx context.Context, nodesPath string, runner *Runner, log *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	// Add the root nodes directory and all existing subdirectories.
	if err := addRecursive(watcher, nodesPath); err != nil {
		watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		var debounce <-chan time.Time

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// If a new directory was created, add it to the watch set so
				// files written inside it trigger future reloads.
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = addRecursive(watcher, event.Name)
					}
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) ||
					event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
					debounce = time.After(200 * time.Millisecond)
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error("node watcher error", "error", err)

			case <-debounce:
				debounce = nil
				nodes, err := LoadNodes(nodesPath)
				if err != nil {
					log.Error("hot reload failed", "path", nodesPath, "error", err)
					continue
				}
				runner.ReloadNodes(nodes)
				log.Info("nodes reloaded", "count", len(nodes), "path", nodesPath)
			}
		}
	}()

	return nil
}

// addRecursive adds path and all of its subdirectories to the watcher.
func addRecursive(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			return watcher.Add(path)
		}
		return nil
	})
}
