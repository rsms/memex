package main

import (
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/rsms/go-testutil"
)

func TestFSEvent(t *testing.T) {
	assert := testutil.NewAssert(t)

	assert.Eq("FSEventCreate == fsnotify.Create", int(FSEventCreate), int(fsnotify.Create))
	assert.Eq("FSEventWrite  == fsnotify.Write", int(FSEventWrite), int(fsnotify.Write))
	assert.Eq("FSEventRemove == fsnotify.Remove", int(FSEventRemove), int(fsnotify.Remove))
	assert.Eq("FSEventRename == fsnotify.Rename", int(FSEventRename), int(fsnotify.Rename))
	assert.Eq("FSEventChmod  == fsnotify.Chmod", int(FSEventChmod), int(fsnotify.Chmod))
}
