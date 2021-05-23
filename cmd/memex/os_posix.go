// +build !android

package main

import (
	"os"
	"strings"
)

func OSShellArgs(command string) []string {
	SHELL := strings.TrimSpace(os.Getenv("SHELL"))
	if SHELL == "" || SHELL[0] != '/' {
		SHELL = "/bin/sh"
	}
	return []string{SHELL, "-c", command}
}
