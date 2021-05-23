package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rsms/go-log"
	"github.com/rsms/memex/extension"
)

var configFilenames = map[string]struct{}{
	"memexservice.yml":  {},
	"memexservice.yaml": {},
}

func isConfigFile(filename string) bool {
	_, ok := configFilenames[filename]
	return ok
}

type ServiceSupervisor struct {
	servicedir string
	log        *log.Logger
	fsw        *FSWatcher
	shutdown   uint32

	mu       sync.RWMutex        // protects the following fields
	servicem map[string]*Service // all known services keyed by serviceId
}

func NewServiceSupervisor(logger *log.Logger, serviceDir string) *ServiceSupervisor {
	return &ServiceSupervisor{
		servicedir: serviceDir,
		log:        logger,
	}
}

func (s *ServiceSupervisor) Start(ctx context.Context) error {
	var err error
	s.servicem = make(map[string]*Service)

	// fs watcher
	if s.fsw, err = NewFSWatcher(ctx); err != nil {
		return err
	}
	s.fsw.Latency = 500 * time.Millisecond
	s.fsw.Add(s.servicedir)

	// initial scan of service directory
	if err := s.initialServiceDirScan(); err != nil {
		return err
	}

	// spawn runloop goroutine
	go func() {
		err := s.runLoop(ctx)
		s.log.Debug("runLoop exited")
		if err != nil {
			s.log.Error("%v", err) // FIXME
		}
	}()
	return nil
}

func (s *ServiceSupervisor) Shutdown() error {
	if !atomic.CompareAndSwapUint32(&s.shutdown, 0, 1) {
		return nil // race lost or already shut down
	}

	// stop fs watcher
	if s.fsw != nil {
		s.fsw.Close()
	}

	// stop services
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.servicem)
	// make a copy of services as they are removed from servicem via stopAndEndService
	services := make([]*Service, len(s.servicem))
	i := 0
	for _, s := range s.servicem {
		services[i] = s
		i++
	}

	// now actually shut down all services
	if n > 0 {
		s.log.Info("stopping %d %s...", n, plural(n, "service", "services"))
		for _, service := range services {
			s.stopAndEndService(service)
		}
		// wait for all services to stop before we return, which may cause the main process to exit
		for _, service := range services {
			service.Wait()
			s.log.Info("service %s stopped", service.id)
		}
	}

	return nil
}

// ---

// stopAndEndService; must hold full lock s.mu
func (s *ServiceSupervisor) stopAndEndService(service *Service) {
	delete(s.servicem, service.id)
	service.StopAndEnd()
}

func (s *ServiceSupervisor) serviceIdFromFile(configfile string) string {
	// e.g. "/memexdir/service/foo/bar/memexservice.yml" => "foo/bar"
	return filepath.Dir(relPath(s.servicedir, configfile))
}

func (s *ServiceSupervisor) getService(sid string) *Service {
	s.mu.RLock()
	service := s.servicem[sid]
	s.mu.RUnlock()
	return service
}

func (s *ServiceSupervisor) getOrAddService(sid, configfile string) *Service {
	service := s.getService(sid)
	if service != nil {
		return service
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	service = s.servicem[sid]
	if service != nil { // data race
		return service
	}
	service, err := LoadServiceFromConfig(sid, configfile, s.log)
	if err != nil {
		s.log.Error("failed to load service %q config (%q): %v", sid, configfile, err)
	}
	if s.log.Level <= log.LevelDebug {
		s.log.Debug("config for service %q:\n%s", sid, service.config.YAMLString())
	}
	s.servicem[sid] = service
	s.log.Debug("new service registered %q", sid)
	return service
}

func (s *ServiceSupervisor) onServiceFileAppeared(configfile string) {
	sid := s.serviceIdFromFile(configfile)
	s.log.Debug("service %q file appeared %q", sid, relPath(s.servicedir, configfile))
	service := s.getOrAddService(sid, configfile)
	if service.config.Disabled {
		if service.IsRunning() {
			s.log.Debug("stopping disabled service %q", sid)
			service.Stop()
		}
	} else {
		if !service.IsRunning() {
			service.Restart()
		}
	}
	s.fsw.Add(configfile)
}

func (s *ServiceSupervisor) onServiceFileDisappeared(configfile string) {
	sid := s.serviceIdFromFile(configfile)
	s.log.Debug("service %q file disappeared %q", sid, relPath(s.servicedir, configfile))
	s.mu.Lock()
	defer s.mu.Unlock()
	if service := s.servicem[sid]; service != nil {
		s.stopAndEndService(service)
	}
}

func (s *ServiceSupervisor) onServiceFileChanged(configfile string) {
	sid := s.serviceIdFromFile(configfile)
	s.log.Debug("service %q file modified %q", sid, relPath(s.servicedir, configfile))

	s.mu.RLock()
	defer s.mu.RUnlock()
	service := s.servicem[sid]
	if service == nil {
		s.log.Debug("unknown service %q", sid)
		return
	}

	config, err := ServiceConfigLoad(configfile)
	if err != nil {
		s.log.Error("failed to load config %q: %v", configfile, err)
		// TODO: should we stop and end the service in this case?
	} else {
		if service.config.IsEqual(config) && !service.CheckExeChangedSinceSpawn() {
			s.log.Info("%q did not change", sid)
		} else {
			service.config = config
			if s.log.Level <= log.LevelDebug {
				s.log.Debug("updated config for service %q:\n%s", sid, service.config.YAMLString())
			}
			if service.config.Disabled {
				if service.IsRunning() {
					s.log.Info("stopping disabled service %q", sid)
					service.Stop()
				}
			} else {
				s.log.Info("restaring service %q", sid)
				service.Restart()
			}
		}
	}
}

func (s *ServiceSupervisor) onServiceFileChange(ev FSEvent) {
	// s.log.Info("fs event: %s %q", ev.Flags.String(), relPath(s.servicedir, ev.Name))
	configfile := ev.Name
	if ev.IsRemove() {
		s.onServiceFileDisappeared(configfile)
	} else {
		s.onServiceFileChanged(configfile)
	}
}

// initial scan of servicedir
func (s *ServiceSupervisor) initialServiceDirScan() error {
	if len(s.servicem) != 0 {
		panic("servicem is not empty")
	}
	s.log.Debug("start initial scan")
	err := filepath.Walk(s.servicedir, func(path string, info os.FileInfo, err error) error {
		// handle error
		if err != nil {
			s.log.Warn("can not read %q: %v", path, err)
			if info.IsDir() {
				return filepath.SkipDir // skip directory
			}
			return nil
		}
		// // debug log
		// if s.log.Level <= log.LevelDebug {
		// 	s.log.Debug("%v  %s", info.Mode(), relPath(s.servicedir, path))
		// }
		// consider service file
		if info.Mode().IsRegular() {
			name := filepath.Base(path)
			if isConfigFile(name) {
				dir := filepath.Dir(path)
				reldir := relPath(s.servicedir, dir)
				s.log.Info("found service config %q", filepath.Join(reldir, name))
				// s.log.Debug("fs watch dir %q", reldir)
				// s.fsw.Add(dir)
				s.onServiceFileAppeared(path)
			}
		}
		return nil
	})
	return err
}

func (s *ServiceSupervisor) runLoop(ctx context.Context) error {
	s.log.Info("watching filesystem for changes")
	for {
		changes, more := <-s.fsw.Events
		if !more {
			break
		}
		s.log.Debug("fs changes: %v", changes)
		for _, ev := range changes {
			if ev.Flags != extension.FSEventChmod {
				s.onServiceFileChange(ev)
			}
		}
	}
	return nil
}

// relPath returns a relative name of path rooted in dir.
// If path is outside dir path is returned verbatim.
// path is assumed to be absolute.
func relPath(dir string, path string) string {
	if len(path) > len(dir) && path[len(dir)] == os.PathSeparator && strings.HasPrefix(path, dir) {
		return path[len(dir)+1:]
	} else if path == dir {
		return "."
	}
	return path
}
