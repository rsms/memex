package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rsms/go-log"
	"github.com/rsms/memex/service-api"
)

const (
	ServiceStateInit  = uint32(iota)
	ServiceStateStart // special state for when the runloop is entered
	ServiceStateRun   // should be running
	ServiceStateStop  // should be stopped
	ServiceStateEnd   // end
)

// defaultRestartLimitInterval is the default value for services without restart-limit-interval
// in their configs
const defaultRestartLimitInterval = 5 * time.Second

// Service is a subprocess that provides some SAM service like processing email.
type Service struct {
	id         string
	cwd        string
	config     *ServiceConfig
	log        *log.Logger
	proc       *exec.Cmd
	ipc        *service.IPC
	runch      chan string
	runstate   uint32      // desired "target" run state
	exepath    string      // absolute path of executable, from args[0], set by spawn()
	runexeStat os.FileInfo // stat at the time of last spawn()

	// procstate is only touched by runloop goroutine
	procstate            uint32
	spawntime            time.Time // time of last spawn() attempt (successful or not)
	restartTimer         *time.Timer
	restartTimerCancelCh chan struct{}
}

func NewService(id, cwd string, supervisorLog *log.Logger) *Service {
	return &Service{
		id:    id,
		cwd:   cwd,
		log:   supervisorLog.SubLogger("[" + id + "]"),
		runch: make(chan string, 64),
	}
}

func LoadServiceFromConfig(id, configfile string, supervisorLog *log.Logger) (*Service, error) {
	s := NewService(id, filepath.Dir(configfile), supervisorLog)
	config, err := ServiceConfigLoad(configfile)
	s.config = config
	return s, err
}

func (s *Service) String() string {
	return s.id
}

// IsRunning returns true if the logical state of the service is "running"
func (s *Service) IsRunning() bool {
	runstate := atomic.LoadUint32(&s.runstate)
	return runstate == ServiceStateStart || runstate == ServiceStateRun
	// return s.proc != nil && (s.proc.ProcessState == nil || s.proc.ProcessState.Exited())
}

// Restart causes the service to restart (or simply start if it's not running)
func (s *Service) Restart() {
	if atomic.CompareAndSwapUint32(&s.runstate, ServiceStateInit, ServiceStateStart) {
		go s.runloop()
	}
	s.runch <- "restart"
}

// Stop causes the service to stop (has no effect if it's not running)
func (s *Service) Stop() {
	s.runch <- "stop"
}

// StopAndEnd stops the process if its running and then ends the service's lifetime.
// The service object is invalid after this call.
// Trying to call methods that change the run state like Restart, Stop etc after calling this
// function will lead to a panic.
func (s *Service) StopAndEnd() {
	s.runch <- "end"
}

// Wait blocks until the service's process has terminated.
// Note: this may block forever if a call to Stop has not been made.
func (s *Service) Wait() int {
	if s.proc == nil {
		return 0
	}
	s.proc.Wait() // ignore error; instead read ProcessState
	// runtime.Gosched()
	return s.proc.ProcessState.ExitCode()
}

func (s *Service) Pid() int {
	if s.proc == nil || s.proc.Process == nil {
		return 0
	}
	return s.proc.Process.Pid
}

func (s *Service) CheckExeChangedSinceSpawn() bool {
	if s.runexeStat == nil || s.exepath == "" {
		return false
	}
	finfo, err := os.Stat(s.exepath)
	if err != nil {
		return true
	}
	// log.Debug("s.exepath: %q", s.exepath)
	// log.Debug("size:  %v <> %v", finfo.Size(), s.runexeStat.Size())
	// log.Debug("mode:  %v <> %v", finfo.Mode(), s.runexeStat.Mode())
	// log.Debug("mtime: %v <> %v", finfo.ModTime(), s.runexeStat.ModTime())
	return finfo.Size() != s.runexeStat.Size() ||
		finfo.Mode() != s.runexeStat.Mode() ||
		finfo.ModTime() != s.runexeStat.ModTime()
}

// ----------------------------------------------------------------------------------
// command & notification handlers

type commandHandler func(s *Service, args [][]byte) ([][]byte, error)
type notificationHandler func(s *Service, args [][]byte)

var (
	notificationHandlers map[string]notificationHandler
	commandHandlers      map[string]commandHandler
)

func init() {
	notificationHandlers = make(map[string]notificationHandler)
	commandHandlers = make(map[string]commandHandler)

	commandHandlers["ping"] = func(_ *Service, args [][]byte) ([][]byte, error) {
		return args, nil
	}

	// commandHandlers["type.register"] = func(s *Service, args [][]byte) ([][]byte, error) {
	// 	var typedefs []service.TypeDef
	// 	if err := json.Unmarshal(args[0], &typedefs); err != nil {
	// 		return nil, err
	// 	}
	// 	doctypes := make([]DocType, len(typedefs))
	// 	for i, t := range typedefs {
	// 		dt := &doctypes[i]
	// 		dt.Id = t.Id
	// 		dt.ParentId = t.ParentId
	// 		dt.Name = t.Name
	// 	}
	// 	err := indexdb.DefineTypes(doctypes...)
	// 	return nil, err
	// }
}

func (s *Service) handleIncomingNotification(name string, args [][]byte) {
	// s.log.Debug("ipc notification: %q, args=%v", name, args)
	if f, ok := notificationHandlers[name]; ok {
		f(s, args)
	} else {
		s.log.Info("unknown notification %q (ignoring)", name)
	}
}

func (s *Service) handleIncomingCommand(cmd string, args [][]byte) ([][]byte, error) {
	// s.log.Debug("ipc command: %q, args=%v", cmd, args)
	if f, ok := commandHandlers[cmd]; ok {
		return f(s, args)
	}
	s.log.Info("received unknown command %q (ignoring)", cmd)
	return nil, errorf("invalid command %q", cmd)
}

// ------------------------------------------------------------
// runloop

var timeInDistantPast = time.Unix(0, 0)

func serviceStateName(state uint32) string {
	switch state {
	case ServiceStateInit:
		return "init"
	case ServiceStateStart:
		return "start"
	case ServiceStateRun:
		return "run"
	case ServiceStateStop:
		return "stop"
	case ServiceStateEnd:
		return "end"
	default:
		return "?"
	}
}

func (s *Service) runchClosed() bool {
	return atomic.LoadUint32(&s.runstate) == ServiceStateEnd
}

func (s *Service) transitionRunState(runstate uint32) bool {
	if s.runstate == runstate {
		return false
	}
	s.log.Debug("runstate transition %s -> %s",
		serviceStateName(s.runstate), serviceStateName(runstate))
	atomic.StoreUint32(&s.runstate, runstate)
	return true
}

func (s *Service) cancelRestartTimer() {
	if s.restartTimer != nil {
		s.log.Debug("cancelling restartTimer")
		s.restartTimerCancelCh <- struct{}{}
		s.restartTimer.Stop()
		select {
		case <-s.restartTimer.C:
		default:
		}
		s.restartTimer = nil
	}
}

func (s *Service) startRestartTimer(delay time.Duration) {
	s.cancelRestartTimer()
	s.restartTimer = time.NewTimer(delay)
	s.restartTimerCancelCh = make(chan struct{}, 1)
	go func() {
		select {
		case _, more := <-s.restartTimer.C:
			s.log.Debug("restartTimer => more: %v", more)
			s.restartTimer = nil
			s.runch <- "continue-spawn"
		case <-s.restartTimerCancelCh:
			s.log.Debug("restartTimer cancelled")
		}
	}()
	return
}

func (s *Service) getRestartLimitInterval() time.Duration {
	if s.config.RestartLimitInterval < 0 {
		return defaultRestartLimitInterval
	}
	return s.config.RestartLimitInterval
}

func (s *Service) dospawn() {
	for {
		restartLimitInterval := s.getRestartLimitInterval()
		timeSinceLastSpawn := time.Since(s.spawntime)
		if timeSinceLastSpawn < restartLimitInterval {
			waittime := restartLimitInterval - timeSinceLastSpawn
			s.log.Info(
				"restarting too quickly; delaying restart by %s imposed by restart-limit-interval=%s",
				waittime.String(), restartLimitInterval)
			s.startRestartTimer(waittime)
			return
		}
		// actually spawn
		if err := s.spawn(); err != nil {
			s.log.Error("spawn failed: %v", err)
			s.transitionProcState(ServiceStateStop)
			// continue for loop which will branch to startRestartTimer
		} else {
			s.transitionProcState(ServiceStateRun)
			return
		}
	}
}

func (s *Service) transitionProcState(nextState uint32) bool {
	if s.procstate == nextState {
		return false
	}
	s.log.Debug("procstate transition %s -> %s",
		serviceStateName(s.procstate), serviceStateName(nextState))
	s.procstate = nextState
	runstate := atomic.LoadUint32(&s.runstate)
	switch s.procstate {
	case ServiceStateRun:
		if runstate == ServiceStateStop {
			s.log.Info("stopping process")
			s.sendSignal(syscall.SIGINT)
		}
	case ServiceStateStop:
		if runstate == ServiceStateRun {
			s.log.Info("spawning new process")
			s.dospawn()
		}
	}
	return true
}

func (s *Service) handleRunloopMsg(msg string) bool {
	s.log.Debug("runloop got message: %v", msg)
	if msg == "restart" {
		// request to start or restart the service
		s.cancelRestartTimer()
		s.transitionRunState(ServiceStateRun)
		// reset spawntime so that a process that terminated early doesn't delay
		// this explicit user-requested restart.
		s.spawntime = timeInDistantPast
		if s.procstate == ServiceStateRun {
			// The process is running; stop it before we can restart it.
			// Zero spawntime so that we can start it again immediately.
			s.killProcess()
		} else {
			// the process is not running; spawn a new process
			s.dospawn()
		}
	} else if msg == "continue-spawn" {
		// dospawn() timer expired
		s.dospawn()
	} else if msg == "stop" {
		// request to stop the service
		s.cancelRestartTimer()
		if s.transitionRunState(ServiceStateStop) {
			s.killProcess()
		}
	} else if msg == "end" {
		// request to stop the service and & end its life, existing the control runloop
		s.cancelRestartTimer()
		if s.transitionRunState(ServiceStateStop) {
			s.killProcess()
		}
		if s.transitionRunState(ServiceStateEnd) {
			close(s.runch)
			return false
		}
	} else if msg == "spawned" {
		// process just started
		s.transitionProcState(ServiceStateRun)
	} else if msg == "exited" {
		// process just exited
		s.transitionProcState(ServiceStateStop)
	} else {
		s.log.Error("runloop got unexpected message %q", msg)
	}
	return true
}

func (s *Service) runloop() {
	s.log.Debug("runloop start")
	if atomic.LoadUint32(&s.runstate) != ServiceStateStart {
		panic("s.state != ServiceStateStart")
	}

loop:
	for {
		select {
		case msg, ok := <-s.runch:
			if !ok || !s.handleRunloopMsg(msg) { // runch closed
				break loop
			}
		}
	}
	s.log.Debug("runloop end")
}

func (s *Service) sendSignal(sig syscall.Signal) bool {
	if s.procstate == ServiceStateRun {
		pid := s.proc.Process.Pid
		s.log.Debug("sending signal %v to process %d", sig, pid)
		// negative pid == process group (not supported on Windows)
		if err := syscall.Kill(-pid, sig); err != nil {
			s.log.Debug("failed to signal process group %d; trying just the process...", -pid)
			if err := syscall.Kill(pid, sig); err != nil {
				s.log.Warn("failed to send signal to process %d: %v", pid, err)
				return false
			}
		}
	}
	return true
}

func (s *Service) killProcess() {
	s.sendSignal(syscall.SIGINT)
	if s.restartTimer != nil {
		s.log.Debug("stopping restartTimer")
		s.restartTimer.Stop()
	}
}

func (s *Service) resolveUserFilePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	var reldir string
	configfile := s.config.GetSourceFilename()
	if configfile != "" {
		reldir = filepath.Dir(configfile)
	} else {
		reldir = MEMEX_DIR
	}
	var err error
	if path, err = filepath.Abs(filepath.Join(reldir, path)); err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		s.log.Warn("failed to resolve path to file %q in dir %q (%v)", path, reldir, err)
	}
	return path, err
}

func (s *Service) resolveExePath(args []string) error {
	// resolve executable path
	s.exepath = args[0]
	if filepath.Base(s.exepath) == s.exepath {
		if lp, err := exec.LookPath(s.exepath); err != nil {
			return err
		} else {
			s.exepath = lp
		}
	} else if !filepath.IsAbs(s.exepath) {
		if fn, err := s.resolveUserFilePath(s.exepath); err == nil {
			s.exepath = fn
		}
	}

	// stat
	finfo, err := os.Stat(s.exepath)
	if err != nil {
		return err
	}
	if finfo.IsDir() {
		return errorf("%s is a directory (expected an executable file)", args[0])
	}
	s.runexeStat = finfo
	return nil
}

func (s *Service) spawn() error {
	if s.ipc != nil {
		panic("spawn with active ipc driver")
	}

	s.spawntime = time.Now() // must update before any return statement

	// get command args from config
	args := s.config.GetStartArgs()
	if len(args) == 0 {
		return errorf("empty args in config or missing \"start\"")
	}

	// resolve executable path; sets s.exepath and s.runexeStat
	err := s.resolveExePath(args)
	if err != nil {
		return err
	}

	// chroot
	chroot := s.config.Chroot
	if chroot != "" {
		if os.Getuid() != 0 {
			s.log.Warn("ignoring chroot in config (memex is not running as root)")
			chroot = ""
		} else {
			chroot, err = s.resolveUserFilePath(chroot)
			if err != nil {
				return err
			}
			s.log.Info("chroot=%q", chroot)
		}
	}

	// environment variables
	env := append(append(os.Environ(), s.config.GetEnvEncodedList()...),
		"MEMEX_IPC_RECV_FD=3",
		"MEMEX_IPC_SEND_FD=4",
	)

	// build process/command
	s.proc = &exec.Cmd{
		Path: s.exepath,
		Args: args,
		Dir:  s.cwd,
		Env:  env,
		SysProcAttr: &syscall.SysProcAttr{
			Chroot: chroot,
			Setsid: true, // start process with its own session id, to support -PID signalling
		},
	}

	// IPC files
	ipcout, ipcRemoteWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	ipcRemoteRead, ipcin, err := os.Pipe()
	if err != nil {
		return err
	}
	// the order of ExtraFiles must be synced with SAM_IPC_RECV_FD & SAM_IPC_SEND_FD above
	// Note: Cmd.ExtraFiles are not available on Windows.
	s.proc.ExtraFiles = []*os.File{ipcRemoteRead, ipcRemoteWrite}
	defer func() {
		// close remote end of IPC pipes
		ipcRemoteRead.Close()
		ipcRemoteWrite.Close()
	}()

	// create stdio pipes for forwarding output
	stdout, err := s.proc.StdoutPipe() // io.ReadCloser
	if err != nil {
		return err
	}
	stderr, err := s.proc.StderrPipe() // io.ReadCloser
	if err != nil {
		return err
	}

	// start process
	s.log.Info("starting process with %q %q", s.exepath, args[1:])
	if err := s.proc.Start(); err != nil {
		return err
	}
	s.spawntime = time.Now() // update again
	s.log.Info("process %d started", s.proc.Process.Pid)
	s.runch <- "spawned"

	s.ipc = service.NewIPC()
	s.ipc.OnCommand = s.handleIncomingCommand
	s.ipc.OnNotification = s.handleIncomingNotification
	s.ipc.Start(ipcout, ipcin)

	// goroutine which reads stdout and stderr, prints line by line with prefix
	go s.readStdioLoop(stdout, "stdout")
	go s.readStdioLoop(stderr, "stderr")

	go func() {
		s.proc.Wait()
		s.ipc.Stop()
		s.ipc = nil
		ps := s.proc.ProcessState
		s.log.Info("process %d exited with status %v", ps.Pid(), ps.ExitCode())
		if !s.runchClosed() {
			// note: s.runch is closed in ServiceStateEnd
			s.runch <- "exited"
		}
	}()

	return nil
}

func (s *Service) readStdioLoop(r io.ReadCloser, ioname string) {
	buffer := make([]byte, 4096)
	tail := []byte{}
	logger := s.log.SubLogger(" [" + ioname + "]")
	for {
		n, err := r.Read(buffer)
		if n == 0 || err == io.EOF {
			s.log.Debug("closed %s (EOF)", ioname)
			break
		}
		if err != nil {
			s.log.Info("error while reading %s of service %s: %s", ioname, s, err)
			break
		}
		// TODO: find line endings and print line by line?
		buf := buffer[:n]
		for {
			i := bytes.IndexByte(buf, '\n')
			if i == -1 {
				tail = append([]byte{}, buf...)
				break
			}
			line := buf[:i]
			buf = buf[i+1:]
			if len(tail) > 0 {
				tail = append(tail, line...)
				logger.Info("%s", string(tail))
				tail = tail[:0]
			} else {
				logger.Info("%s", string(line))
			}
		}
	}
}
