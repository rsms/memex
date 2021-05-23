package main

func OSShellArgs(command string) []string {
	return []string{"/system/bin/sh", "-c", command}
}
