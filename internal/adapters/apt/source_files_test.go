package apt

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestSourcePackageFiles(t *testing.T) {
	sum := strings.Repeat("a", 64)
	body := fmt.Sprintf("Package: demo\nDirectory: pool/main/d/demo\nChecksums-Sha256:\n %s 3 demo.dsc\n %s 4 demo.tar.xz\n\n", sum, sum)
	var paths []string
	err := parseDeb822(strings.NewReader(body), func(rec map[string]string) error {
		return sourcePackageFiles(rec, func(logical string, size int64, digest string) error {
			if digest != sum || size != int64(len(paths)+3) {
				t.Fatalf("wrong record: %s %d %s", logical, size, digest)
			}
			paths = append(paths, logical)
			return nil
		})
	})
	if err != nil || strings.Join(paths, ",") != "pool/main/d/demo/demo.dsc,pool/main/d/demo/demo.tar.xz" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	for _, bad := range []string{"\n", "\ninvalid", "\n" + sum + " -1 demo.dsc", "\n" + sum + " 3 ../demo.dsc"} {
		err := sourcePackageFiles(map[string]string{"Package": "demo", "Directory": "pool/main/d/demo", "Checksums-Sha256": bad}, func(string, int64, string) error {
			t.Fatal("invalid file record accepted")
			return nil
		})
		if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "demo") {
			t.Fatalf("bad record %q: %v", bad, err)
		}
	}
}
