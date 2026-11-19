package apt

import (
	"strings"
	"testing"
)

func TestReleaseIndexesListsCheckedFiles(t *testing.T) {
	sum := strings.Repeat("a", 64)
	body := []byte("Origin: Example\nSHA256:\n " + sum + " 4 main/binary-amd64/Packages\n")
	items, err := ReleaseIndexes(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Path != "main/binary-amd64/Packages" || items[0].Size != 4 || items[0].SHA256 != sum {
		t.Fatalf("unexpected Release indexes: %+v", items)
	}
}
func TestReleaseIndexesRejectsEscapingPath(t *testing.T) {
	sum := strings.Repeat("a", 64)
	body := []byte("SHA256:\n " + sum + " 4 ../outside\n")
	if _, err := ReleaseIndexes(body); err == nil {
		t.Fatal("unsafe Release paths must be rejected")
	}
}
