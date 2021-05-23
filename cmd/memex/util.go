package main

import (
	"fmt"
	"os"
)

// create error
func errorf(format string, v ...interface{}) error {
	return fmt.Errorf(format, v...)
}

// log error and exit
func fatalf(msg interface{}, arg ...interface{}) {
	var format string
	if s, ok := msg.(string); ok {
		format = s
	} else if s, ok := msg.(fmt.Stringer); ok {
		format = s.String()
	} else {
		format = fmt.Sprintf("%v", msg)
	}
	fmt.Fprintf(os.Stderr, format+"\n", arg...)
	os.Exit(1)
}

// isdir returns nil if path is a directory, or an error describing the issue
func isdir(path string) error {
	finfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !finfo.IsDir() {
		return errorf("%q is not a directory", path)
	}
	return nil
}

func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}
