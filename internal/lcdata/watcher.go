package lcdata

import (
	"context"
	"log/slog"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchNodes watches the nodes directory and hot-reloads the runner's node
// registry whenever a file is created, modified, removed, or renamed.
// It debounces rapid bursts of events with a 200ms cooldown.
// The goroutine exits when ctx is cancelled.
func WatchNodes(ctx context.Context, nodesPath string, runner *Runner, log *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(nodesPath); err != nil {
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
