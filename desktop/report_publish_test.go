package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishReportFile(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, "report.partial")
	destination := filepath.Join(dir, "report.json")
	if err := os.WriteFile(staged, []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishReportFile(staged, destination); err == nil {
		t.Fatal("must not overwrite an existing report")
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "original" {
		t.Fatalf("existing report changed: %q, %v", got, err)
	}
	if err = os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err = publishReportFile(staged, destination); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(destination)
	if err != nil || string(got) != "complete" {
		t.Fatalf("published report is incorrect: %q, %v", got, err)
	}
}
