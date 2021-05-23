package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rsms/memex/extension"
)

// event op (bitmask)
const (
	FSEventCreate = extension.FSEventCreate
	FSEventWrite  = extension.FSEventWrite
	FSEventRemove = extension.FSEventRemove
	FSEventRename = extension.FSEventRename
	FSEventChmod  = extension.FSEventChmod
)

type FSEvent = extension.FSEvent
type FSEventFlags = extension.FSEventFlags

// FSWatcher observes file system for changes.
// A new FSWatcher starts in Resumed mode.
type FSWatcher struct {
	Latency time.Duration  // Time window for bundling changes together. Default: 100ms
	Events  chan []FSEvent // Channel on which event batches are delivered
	Error   error          // After Events channel is closed, this indicates the reason

	w     *fsnotify.Watcher
	start uint32
	ctx   context.Context
}

func NewFSWatcher(ctx context.Context) (*FSWatcher, error) {
	w2, err := fsnotify.NewWatcher()
	if err != nil {
		// fails only when the underlying OS extension fails.
		// E.g. golang.org/x/sys/unix.{Kqueue,InotifyInit1} or syscall.CreateIoCompletionPort
		return nil, err
	}
	w := &FSWatcher{
		Latency: 100 * time.Millisecond,
		Events:  make(chan []FSEvent),
		w:       w2,
		ctx:     ctx,
	}
	return w, nil
}

// Add file or directory (non-recursively) to be watched.
// If path is already watched nil is returned (duplicates ignored.)
func (w *FSWatcher) Add(path string) error {
	err := w.w.Add(path)
	if err == nil && atomic.CompareAndSwapUint32(&w.start, 0, 1) {
		go w.runLoop()
	}
	return err
}

// Remove unregisters a file or directory that was previously registered with Add.
// If path is not being watched an error is returned.
func (w *FSWatcher) Remove(path string) error {
	return w.w.Remove(path)
}

// Stop begins the shutdown process of the watcher. Causes Run() to return.
func (w *FSWatcher) Close() error {
	if atomic.CompareAndSwapUint32(&w.start, 0, 1) {
		// never started so w.Events would not close from runLoop exiting
		close(w.Events)
	}
	err := w.w.Close()
	if err == nil {
		err = w.Error
	}
	return err
}

func (w *FSWatcher) runLoop() {
	changeq := make(map[string]FSEventFlags)
	foreverDuration := time.Duration(0x7fffffffffffffff)
	flushTimer := time.NewTimer(foreverDuration)
	flushTimerActive := false
	var errCounter int

	defer close(w.Events)

	for {
		select {

		case <-w.ctx.Done():
			w.Error = w.ctx.Err()
			return

		case <-flushTimer.C:
			if len(changeq) > 0 {
				// logd("[fswatcher] flush %+v", changeq)
				events := make([]FSEvent, 0, len(changeq))
				for name, flags := range changeq {
					events = append(events, FSEvent{
						Flags: flags,
						Name:  name,
					})
				}
				for _, ev := range events {
					delete(changeq, ev.Name)
				}
				w.Events <- events
			}
			flushTimer.Reset(foreverDuration)
			flushTimerActive = false

		case event, more := <-w.w.Events:
			if !more {
				// closed
				return
			}

			// logd("[fswatcher] event: %v %q", event.Op, event.Name)

			// reset error counter
			errCounter = 0

			// update event mask
			prev := changeq[event.Name]
			next := FSEventFlags(event.Op)
			if prev&FSEventCreate != 0 && next&FSEventRemove != 0 && next&FSEventCreate == 0 {
				// was created and is now removed
				next = ((prev | next) &^ FSEventCreate) &^ FSEventWrite
			} else if prev&FSEventRemove != 0 && next&FSEventCreate != 0 && next&FSEventRemove == 0 {
				// was removed and is now created -> modified
				next = (prev | FSEventWrite) &^ FSEventRemove
			} else {
				next = prev | next
			}
			changeq[event.Name] = next

			if !flushTimerActive {
				flushTimerActive = true
				flushTimer.Reset(w.Latency)
			}

		case err := <-w.w.Errors:
			w.Error = err
			errCounter++
			if errCounter > 10 {
				// There were many errors without any file events.
				// Close the watcher and return as there may be unrecoverable
				w.Close()
				return
			}

		} // select
	}
}
