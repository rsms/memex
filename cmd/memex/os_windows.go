package main

import (
	"os"
	"strings"
)

func OSShellArgs(command string) []string {
	args := []string{os.Getenv("comspec")}
	if len(args[0]) == 0 {
		args[0] = "cmd.exe"
	}
	if strings.Contains(args[0], "cmd.exe") {
		command = strings.ReplaceAll(command, "\"", "\\\"")
		args = append(args, "/d", "/s", "/c", "\""+command+"\"")
	} else {
		args = append(args, "-c", command)
	}
	return args
}
