package server

import (
	"errors"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestImportSourceGuard(t *testing.T) {
	api := &API{}
	release, err := api.acquireImportSource("source-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.acquireImportSource("source-a"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second import mutation must conflict: %v", err)
	}
	other, err := api.acquireImportSource("source-b")
	if err != nil {
		t.Fatalf("independent source was blocked: %v", err)
	}
	other()
	release()
	again, err := api.acquireImportSource("source-a")
	if err != nil {
		t.Fatalf("guard was not released: %v", err)
	}
	again()
}
