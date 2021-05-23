package service

//
// IPC implements inter-process communication over io.Reader and io.Writer.
//
// Message wire format:
//   Msg          = u32(len) MsgType u32(id) u32(cmdlen) string(cmd) u32(argc) Arg*
//   Arg          = u32(len) []byte(value)
//   MsgType      = CommandType | ResultType
//   CommandType  = byte(">")
//   ResultType   = byte("<")
//
// Command example:
//   <26> ">" <1> <4> "ping" <1> <5> "hello"
//
// Result examples:
//   <24> "<" <1> <2> "ok" <1> <5> "hello"
//   <39> "<" <1> <5> "error" <1> <17> "error description"
//
import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
)

type IPCCommandHandler func(cmd string, args [][]byte) ([][]byte, error)
type IPCNotificationHandler func(name string, args [][]byte)

type IPC struct {
	OnCommand      IPCCommandHandler
	OnNotification IPCNotificationHandler
	Running        bool // true in-between calls to Start() and Stop()

	stopch  chan bool    // stop signal
	writech chan *ipcMsg // outgoing messages
	reqid   uint32       // next request id
	reqw    []ipcPromise // pending outgoing requests' response channels
	reqwmu  sync.Mutex   // lock for reqm
}

type ipcMsg struct {
	typ  byte
	id   uint32
	cmd  string
	args [][]byte
}

type ipcPromise struct {
	id uint32
	ch chan *ipcMsg // response channel
}

const (
	msgTypeCommand      = byte('>')
	msgTypeResult       = byte('<')
	msgTypeNotification = byte('N')
)

var (
	ErrNoMemex      error // returned from Connect when Memex is unavailable
	ErrNotConnected error
)

// default IPC object, created by Connect and used by package-level functions
var DefaultIPC *IPC

// true when DefaultIPC is valid; after a successful call to Connect()
var Connected bool

func init() {
	ErrNoMemex = errors.New("not running in Memex (ErrNoMemex)")
	ErrNotConnected = errors.New("not connected to Memex (ErrNotConnected)")
}

// connect DefaultIPC
func Connect(onCommand func(cmd string, args [][]byte) ([][]byte, error)) error {
	if DefaultIPC != nil {
		return fmt.Errorf("already connected")
	}
	// check if this process is running as a Memex subprocess
	recvfd, err := strconv.Atoi(os.Getenv("MEMEX_IPC_RECV_FD"))
	if err != nil {
		return ErrNoMemex
	}
	sendfd, err := strconv.Atoi(os.Getenv("MEMEX_IPC_SEND_FD"))
	if err != nil {
		return ErrNoMemex
	}
	DefaultIPC = NewIPC()
	DefaultIPC.OnCommand = onCommand
	ipcin := os.NewFile(uintptr(recvfd), "ipcrecv")
	ipcout := os.NewFile(uintptr(sendfd), "ipcsend")
	if err := DefaultIPC.Start(ipcin, ipcout); err != nil {
		return err
	}
	Connected = true
	return err
}

func Command(cmd string, args ...[]byte) (res [][]byte, err error) {
	if DefaultIPC == nil {
		return nil, ErrNotConnected
	}
	return DefaultIPC.Command(cmd, args...)
}

func JsonCommand(cmd string, arg0 interface{}, args ...[]byte) (res [][]byte, err error) {
	arg0buf, err := json.Marshal(arg0)
	if err != nil {
		return nil, err
	}
	if len(args) > 0 {
		args2 := append([][]byte{arg0buf}, args...)
		return DefaultIPC.Command(cmd, args2...)
	}
	return DefaultIPC.Command(cmd, arg0buf)
}

func Notify(name string, args ...[]byte) error {
	if DefaultIPC == nil {
		return ErrNotConnected
	}
	return DefaultIPC.Notify(name, args...)
}

func NewIPC() *IPC {
	return &IPC{
		stopch:  make(chan bool, 1),
		writech: make(chan *ipcMsg, 8),
	}
}

func (ipc *IPC) Notify(name string, args ...[]byte) error {
	if !ipc.Running {
		return fmt.Errorf("ipc not running")
	}
	ipc.writech <- &ipcMsg{typ: msgTypeNotification, cmd: name, args: args}
	return nil
}

func (ipc *IPC) CommandAndForget(cmd string, args ...[]byte) error {
	if !ipc.Running {
		return fmt.Errorf("ipc not running")
	}
	id := atomic.AddUint32(&ipc.reqid, 1)
	ipc.writech <- &ipcMsg{typ: msgTypeCommand, id: id, cmd: cmd, args: args}
	return nil
}

func (ipc *IPC) Command(cmd string, args ...[]byte) (res [][]byte, err error) {
	if !ipc.Running {
		return nil, fmt.Errorf("ipc not running")
	}

	// generate request ID and create response channel
	id := atomic.AddUint32(&ipc.reqid, 1)
	ch := make(chan *ipcMsg)

	// add wait object
	ipc.reqwmu.Lock()
	ipc.reqw = append(ipc.reqw, ipcPromise{id: id, ch: ch})
	ipc.reqwmu.Unlock()

	// send request
	ipc.writech <- &ipcMsg{typ: msgTypeCommand, id: id, cmd: cmd, args: args}

	// wait for response
	msg := <-ch

	if msg.cmd == "error" {
		if len(msg.args) > 0 {
			err = fmt.Errorf("ipc error: %s", string(msg.args[0]))
		} else {
			err = fmt.Errorf("ipc error")
		}
	} else {
		res = msg.args
	}
	return
}

func (ipc *IPC) Stop() {
	if ipc.Running {
		ipc.Running = false
		ipc.stopch <- true
	}
}

func (ipc *IPC) Start(r io.Reader, w io.Writer) error {
	if ipc.Running {
		return fmt.Errorf("already started")
	}
	ipc.Running = true
	go func() {
		err := ipc.ioLoop(r, w)
		ipc.Running = false
		if err != nil {
			logf("[memex/ipc] I/O error: %v", err)
		}
	}()
	return nil
}

func (ipc *IPC) ioLoop(r io.Reader, w io.Writer) error {
	// start response writer goroutine
	go ipc.writeLoop(w)

	// read buffer and total accumulative input
	buf := make([]byte, 4096)
	input := []byte{}

	// read loop
	for {
		// Semantics of Read():
		// - if the underlying read buffer is empty, wait until its filled and then return.
		// - else return the underlying buffer immediately
		// This means that fflush in the service process causes Read here to return.
		n, err := r.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		} else if n == 0 || !ipc.Running {
			break
		}

		input = append(input, buf[:n]...)

		// parse each complete request in input
		bytes := input
		for {
			msgdata, remainder, ok := readLengthPrefixedSlice(bytes)
			if !ok {
				break
			}
			bytes = remainder

			// Clone the input and run it on another goroutine
			data := append([]byte{}, msgdata...)
			go ipc.readMsg(data)
		}

		// Move the remaining partial request to the end to avoid reallocating
		input = append(input[:0], bytes...)
	} // for
	// EOF or stopped
	return nil
}

func writeMsg(w io.Writer, buf []byte, msg *ipcMsg) {
	length := 1 + 4 + 4 + len(msg.cmd) + 4 // MsgType + u32(id) + u32(cmdlen) + len(cmd) + u32(argc)
	for _, v := range msg.args {
		length += 4 + len(v)
	}

	// write u32(len) byte('>') u32(id) u32(len)
	offs := uint32(0)
	binary.LittleEndian.PutUint32(buf, uint32(length))
	offs += 4
	buf[offs] = msg.typ
	offs += 1
	binary.LittleEndian.PutUint32(buf[offs:], msg.id)
	offs += 4
	binary.LittleEndian.PutUint32(buf[offs:], uint32(len(msg.cmd)))
	offs += 4
	w.Write(buf[:offs])
	offs = 0 // flush
	w.Write([]byte(msg.cmd))
	binary.LittleEndian.PutUint32(buf, uint32(len(msg.args)))
	w.Write(buf[:4])

	// write args
	for _, v := range msg.args {
		binary.LittleEndian.PutUint32(buf, uint32(len(v)))
		w.Write(buf[:4])
		w.Write(v)
	}
}

func (ipc *IPC) readMsg(data []byte) {
	defer func() {
		if r := recover(); r != nil {
			logf("[memex/ipc] ignoring invalid data: %v\n%s", r, debug.Stack())
		}
	}()

	// read header
	// Note: readMsg receives data slice that _excludes_ the length prefix (no u32(len) header)
	offs := uint32(0)
	typ := data[offs]
	offs += 1
	id := binary.LittleEndian.Uint32(data[offs : offs+4])
	offs += 4
	cmdlen := binary.LittleEndian.Uint32(data[offs : offs+4])
	offs += 4
	cmd := data[offs : offs+cmdlen]
	offs += cmdlen
	argc := binary.LittleEndian.Uint32(data[offs : offs+4])
	offs += 4

	// read args
	args := [][]byte{}
	for i := uint32(0); i < argc; i++ {
		z := binary.LittleEndian.Uint32(data[offs : offs+4])
		offs += 4
		args = append(args, data[offs:offs+z])
		offs += z
	}

	// dispatch
	switch typ {

	case msgTypeNotification:
		if ipc.OnNotification == nil {
			return
		}
		func() {
			// trap panics in OnCommand and create error response
			defer func() {
				if r := recover(); r != nil {
					logf("[memex/ipc] recovered panic in OnNotification: %v\n%s", r, debug.Stack())
				}
			}()
			ipc.OnNotification(string(cmd), args)
		}()

	case msgTypeCommand:
		if ipc.OnCommand == nil {
			ipc.sendErrorResult(id, "commands not accepted")
			return
		}
		func() {
			// trap panics in OnCommand and create error response
			defer func() {
				if r := recover(); r != nil {
					logf("[memex/ipc] recovered panic in OnCommand: %v\n%s", r, debug.Stack())
					ipc.sendErrorResult(id, r)
				}
			}()
			res, err := ipc.OnCommand(string(cmd), args) // -> [][]byte, error
			if err != nil {
				ipc.sendErrorResult(id, err)
			} else {
				//logf("IPC readMsg enqueue response on writech")
				ipc.writech <- &ipcMsg{
					typ:  msgTypeResult,
					id:   id,
					cmd:  "ok",
					args: res,
				}
			}
		}()

	case msgTypeResult:
		// find corresponding wait object
		var reqwait *ipcPromise
		ipc.reqwmu.Lock()
		for i, v := range ipc.reqw {
			if v.id == id {
				reqwait = &v
				ipc.reqw = append(ipc.reqw[:i], ipc.reqw[i+1:]...)
			}
		}
		ipc.reqwmu.Unlock()
		if reqwait != nil {
			reqwait.ch <- &ipcMsg{
				typ:  typ,
				id:   id,
				cmd:  string(cmd),
				args: args,
			}
		}

	default:
		logf("[memex/ipc] readMsg ignoring invalid message type 0x%02x", typ)

	}
}

func (ipc *IPC) sendErrorResult(id uint32, err interface{}) {
	ipc.writech <- &ipcMsg{
		typ:  msgTypeResult,
		id:   id,
		cmd:  "error",
		args: [][]byte{[]byte(fmt.Sprint(err))},
	}
}

func (ipc *IPC) writeLoop(w io.Writer) {
	// buf is a small buffer used temporarily to encode data
	buf := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for {
		// logf("IPC writeLoop awaiting message...")
		select {
		case msg := <-ipc.writech:
			// logf("IPC writeLoop writing message %v", msg)
			writeMsg(w, buf, msg)
		case <-ipc.stopch:
			// IPC shutting down -- exit write loop
			break
		}
	}
}

func writeU32(w io.Writer, v uint32) {
	buf := []byte{0, 0, 0, 0}
	binary.LittleEndian.PutUint32(buf, v)
	w.Write(buf)
}

func readLengthPrefixedSlice(input []byte) (data []byte, remainder []byte, ok bool) {
	inlen := len(input)
	if inlen >= 4 {
		z := binary.LittleEndian.Uint32(input)
		if uint(inlen-4) >= uint(z) {
			return input[4 : 4+z], input[4+z:], true
		}
	}
	return []byte{}, input, false
}

func logf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", v...)
}
