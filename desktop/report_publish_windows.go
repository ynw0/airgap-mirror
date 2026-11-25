//go:build windows

package main

import "syscall"

// MoveFile fails if the destination exists. Unlike os.Rename on Windows,
// it cannot replace a report that appeared while the export was running.
func publishReportFile(staged, destination string) error {
	from, err := syscall.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return syscall.MoveFile(from, to)
}
