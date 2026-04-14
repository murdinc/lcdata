package lcdata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func executeFile(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	events chan<- Event,
) (map[string]any, error) {

	// Path comes from input or node config
	path := stringVal(inputs, "path")
	if path == "" {
		return nil, fmt.Errorf("input.path is required for file nodes")
	}

	// Clean path to prevent directory traversal
	path = filepath.Clean(path)

	switch node.Operation {
	case "read":
		return fileRead(ctx, path)
	case "write":
		return fileWrite(ctx, path, inputs, false)
	case "append":
		return fileWrite(ctx, path, inputs, true)
	case "exists":
		return fileExists(ctx, path)
	case "delete":
		return fileDelete(ctx, path)
	case "list":
		return fileList(ctx, path)
	default:
		return nil, fmt.Errorf("unknown file operation: %q (supported: read, write, append, exists, delete, list)", node.Operation)
	}
}

func fileRead(_ context.Context, path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	info, _ := os.Stat(path)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	return map[string]any{
		"content": string(data),
		"path":    path,
		"size":    size,
	}, nil
}

func fileWrite(_ context.Context, path string, inputs map[string]any, append bool) (map[string]any, error) {
	content := stringVal(inputs, "content")

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	flags := os.O_WRONLY | os.O_CREATE
	if append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer f.Close()

	n, err := f.WriteString(content)
	if err != nil {
		return nil, fmt.Errorf("failed to write to file %s: %w", path, err)
	}

	return map[string]any{
		"path":          path,
		"bytes_written": n,
		"ok":            true,
	}, nil
}

func fileExists(_ context.Context, path string) (map[string]any, error) {
	_, err := os.Stat(path)
	exists := !os.IsNotExist(err)

	size := int64(0)
	isDir := false
	if exists {
		if info, serr := os.Stat(path); serr == nil {
			size = info.Size()
			isDir = info.IsDir()
		}
	}

	return map[string]any{
		"exists": exists,
		"path":   path,
		"size":   size,
		"is_dir": isDir,
	}, nil
}

func fileDelete(_ context.Context, path string) (map[string]any, error) {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]any{
				"path": path,
				"ok":   false,
			}, nil
		}
		return nil, fmt.Errorf("failed to delete file %s: %w", path, err)
	}
	return map[string]any{
		"path": path,
		"ok":   true,
	}, nil
}

func fileList(_ context.Context, path string) (map[string]any, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory %s: %w", path, err)
	}

	files := make([]any, 0, len(entries))
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		files = append(files, map[string]any{
			"name":   e.Name(),
			"is_dir": e.IsDir(),
			"size":   size,
		})
	}

	return map[string]any{
		"path":  path,
		"files": files,
		"count": len(files),
	}, nil
}
