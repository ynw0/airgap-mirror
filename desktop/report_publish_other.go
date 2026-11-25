//go:build !windows

package main

import "os"

// Link publishes without overwriting. The caller removes the staging name.
func publishReportFile(staged, destination string) error {
	return os.Link(staged, destination)
}
