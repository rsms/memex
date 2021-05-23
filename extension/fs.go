package extension

import (
	"fmt"
	"time"
)

type FSWatcher interface {
	Add(name string) error
	Remove(name string) error
	Events() <-chan []FSEvent
	Latency() time.Duration
	SetLatency(time.Duration)
}

type FSEvent struct {
	Name  string       // filename
	Flags FSEventFlags // one or more FSEvent flag
}

func (ev FSEvent) IsCreate() bool { return ev.Flags&FSEventCreate != 0 }
func (ev FSEvent) IsWrite() bool  { return ev.Flags&FSEventWrite != 0 }
func (ev FSEvent) IsRemove() bool { return ev.Flags&FSEventRemove != 0 }
func (ev FSEvent) IsRename() bool { return ev.Flags&FSEventRename != 0 }
func (ev FSEvent) IsChmod() bool  { return ev.Flags&FSEventChmod != 0 }

type FSEventFlags uint32

const (
	FSEventCreate FSEventFlags = 1 << iota
	FSEventWrite
	FSEventRemove
	FSEventRename
	FSEventChmod
)

func (ev FSEvent) String() string {
	return fmt.Sprintf("%q %s", ev.Name, ev.Flags.String())
}

func (fl FSEventFlags) String() string {
	a := [33]byte{} // max: "|CREATE|REMOVE|WRITE|RENAME|CHMOD"
	buf := a[:0]
	if fl&FSEventCreate != 0 {
		buf = append(buf, "|CREATE"...)
	}
	if fl&FSEventRemove != 0 {
		buf = append(buf, "|REMOVE"...)
	}
	if fl&FSEventWrite != 0 {
		buf = append(buf, "|WRITE"...)
	}
	if fl&FSEventRename != 0 {
		buf = append(buf, "|RENAME"...)
	}
	if fl&FSEventChmod != 0 {
		buf = append(buf, "|CHMOD"...)
	}
	if len(buf) == 0 {
		return "0"
	}
	return string(buf[1:]) // sans "|"
}
