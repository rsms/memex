package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
)

// UNIX file mode permission bits
type UnixPermBits uint32

const (
	UnixPermSetuid UnixPermBits = 1 << (12 - 1 - iota)
	UnixPermSetgid
	UnixPermSticky
	UnixPermUserRead
	UnixPermUserWrite
	UnixPermUserExecute
	UnixPermGroupRead
	UnixPermGroupWrite
	UnixPermGroupExecute
	UnixPermOtherRead
	UnixPermOtherWrite
	UnixPermOtherExecute
)

func mkdirsForFile(filename string, perm os.FileMode) error {
	dir, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	dirperm := matchingDirUnixPerm(perm)
	return os.MkdirAll(filepath.Dir(dir), dirperm)
}

func renameFile(oldpath, newpath string, mode os.FileMode) error {
	if err := os.Chmod(oldpath, mode); err != nil {
		return err
	}
	err := os.Rename(oldpath, newpath)
	if err != nil && os.IsNotExist(err) {
		if err = mkdirsForFile(newpath, mode); err == nil {
			err = os.Rename(oldpath, newpath)
		}
	}
	return err
}

func writeFileWithMkdirs(filename string, data []byte, mode os.FileMode) error {
	retried := false
	for {
		if err := ioutil.WriteFile(filename, data, mode); err != nil {
			if retried || !os.IsNotExist(err) {
				return err
			}
			retried = true
			if err := mkdirsForFile(filename, mode); err != nil {
				return err
			}
			continue
		}
		break
	}
	return nil
}

func matchingDirUnixPerm(perm os.FileMode) os.FileMode {
	if perm&os.FileMode(UnixPermUserRead) != 0 {
		perm |= os.FileMode(UnixPermUserExecute)
	}
	if perm&os.FileMode(UnixPermGroupRead) != 0 {
		perm |= os.FileMode(UnixPermGroupExecute)
	}
	if perm&os.FileMode(UnixPermOtherRead) != 0 {
		perm |= os.FileMode(UnixPermOtherExecute)
	}
	return perm
}
