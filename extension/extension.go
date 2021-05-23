package extension

import (
	"context"

	"github.com/rsms/go-log"
)

// Extension represents a specific extension instance and provides an API to memex
type Extension interface {
	Name() string             // extension name
	Version() string          // instance version
	Context() context.Context // the context of the extension instance
	Done() <-chan struct{}    // closes when the extension should stop (==Context().Done())
	Logger() *log.Logger      // logger for this extension

	// WatchFiles returns a new file watcher which watches the file system for changes
	// to the provided paths. The returned watcher operates within the instance's context.
	WatchFiles(path ...string) FSWatcher
}

// RunFunc is the type of function that a extension exposes as it's "main" function.
// The extension should not return from this function until Insance.Done() is closed.
type RunFunc func(Extension)

// Register a extension. Must be run at init time (i.e. in an init() function)
func Register(extensionName string, run RunFunc) {
	if _, ok := Registry[extensionName]; ok {
		panic("duplicate extension \"" + extensionName + "\"")
	}
	Registry[extensionName] = run
}

// Don't modify from a extension
var Registry = map[string]RunFunc{}
