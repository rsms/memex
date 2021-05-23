package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rsms/go-log"
	"github.com/rsms/memex/extension"
)

// —————————————————————————————————————————————————————————————————————————————————————

// implementation of extension.Extension
type Extension struct {
	name    string
	version string
	ctx     context.Context

	// fields used only by supervisor; never exposed to the extension itself
	cancel context.CancelFunc // cancel function for ctx
	donech chan struct{}      // closed when a extension's run function returns

	lazymu sync.Mutex // protects the following fields (lazy-initialized data)
	logger *log.Logger
}

// check conformance to extension.Extension
var _ extension.Extension = &Extension{}

func (si *Extension) Name() string             { return si.name }
func (si *Extension) Version() string          { return si.version }
func (si *Extension) Done() <-chan struct{}    { return si.ctx.Done() }
func (si *Extension) Context() context.Context { return si.ctx }

func (si *Extension) Logger() *log.Logger {
	si.lazymu.Lock()
	defer si.lazymu.Unlock()
	if si.logger == nil {
		si.logger = log.SubLogger("[" + si.String() + "]")
	}
	return si.logger
}

func (si *Extension) WatchFiles(paths ...string) extension.FSWatcher {
	// TODO maybe share a watcher (Each FSWatcher uses considerable resources)
	w1, err := NewFSWatcher(si.ctx)
	if err != nil {
		// fails only when the underlying OS extension fails; we are generally in trouble.
		panic(err)
	}
	w := &ServiceFSWatcher{FSWatcher: *w1}
	for _, path := range paths {
		w.Add(path)
	}
	return w
}

func (si *Extension) String() string {
	return fmt.Sprintf("%s@%s", si.name, si.version)
}

// —————————————————————————————————————————————————————————————————————————————————————

type ServiceFSWatcher struct {
	FSWatcher
}

func (w *ServiceFSWatcher) Events() <-chan []FSEvent {
	return w.FSWatcher.Events
}
func (w *ServiceFSWatcher) Latency() time.Duration {
	return w.FSWatcher.Latency
}
func (w *ServiceFSWatcher) SetLatency(latency time.Duration) {
	w.FSWatcher.Latency = latency
}

// —————————————————————————————————————————————————————————————————————————————————————

type ExtSupervisor struct {
	runmu  sync.RWMutex
	runmap map[string]*Extension
	log    *log.Logger
}

func NewExtSupervisor(l *log.Logger) *ExtSupervisor {
	return &ExtSupervisor{
		runmap: make(map[string]*Extension),
		log:    l,
	}
}

// Start starts all extensions described in registry.
func (s *ExtSupervisor) Start(ctx context.Context, registry map[string]extension.RunFunc) error {
	s.runmu.Lock()
	defer s.runmu.Unlock()
	for name, runf := range registry {
		s.startService(ctx, name, runf)
	}
	return nil
}

// Shutdown stops all extensions
func (s *ExtSupervisor) Shutdown(shutdownCtx context.Context) error {
	// hold a lock during entire "stop all" process to prevent stopService or startService
	// calls from interfering.
	s.runmu.Lock()
	defer s.runmu.Unlock()
	return s.stopAllServices(shutdownCtx)
}

// startService creates a new extension Extension and runs it via runService in a new goroutine.
// NOT THREAD SAFE: Caller must hold lock on s.runmu
func (s *ExtSupervisor) startService(ctx context.Context, name string, runf extension.RunFunc) {
	extensionCtx, cancel := context.WithCancel(ctx)
	si := &Extension{
		name:    name,
		version: "1", // TODO
		ctx:     extensionCtx,
		cancel:  cancel,
		donech:  make(chan struct{}),
	}
	s.log.Info("starting extension %s", si)
	s.runmap[name] = si

	// start the extension goroutine, which calls runf and blocks until it either:
	// - returns,
	// - panics (logs panic), or
	// - times out (si.ctx)
	go func() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("panic in extension %q: %v\n%s", si.name, r, debug.Stack())
				}
			}()
			runf(si)
			select {
			case <-si.ctx.Done(): // ok
				s.log.Debug("extension %s exited", si)
			default:
				s.log.Warn("extension %s exited prematurely (did not wait for Done())", si)
				si.cancel()
			}
		}()

		// signal to supervisor that the extension has completed shutdown
		close(si.donech)
	}()
}

// stopService stops a specific extension.
// Thread-safe.
func (s *ExtSupervisor) stopService(ctx context.Context, name string) error {
	// retrieve and remove extension instance from runmap
	s.runmu.Lock()
	si := s.runmap[name]
	if si != nil {
		delete(s.runmap, name)
	}
	s.runmu.Unlock()
	if si == nil {
		return errorf("can not find extension %q", name)
	}

	// cancel the extension and await its shutdown
	si.cancel()
	select {
	case <-si.donech:
		// ok; extension shut down ok
	case <-ctx.Done():
		// Context cancelled or timed out
		// NOTE: There is a possibility that an ill-behaving extension just keeps on truckin' here.
		return ctx.Err()
	}
	return nil
}

// stopService stops all extensions.
// NOT THREAD SAFE: Caller must hold lock on s.runmu
func (s *ExtSupervisor) stopAllServices(ctx context.Context) error {
	// cancel all extensions
	for _, si := range s.runmap {
		si.cancel()
	}

	// wait for extensions to stop
	var err error
wait_loop:
	for _, si := range s.runmap {
		select {
		case <-si.donech: // ok; extension shut down in time
		case <-ctx.Done():
			s.log.Warn("timeout while waiting for extension %s to shut down", si)
			err = ctx.Err()
			break wait_loop
		}
	}

	// clear runmap
	s.runmap = nil
	return err
}
