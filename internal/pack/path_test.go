package pack

import "testing"

func TestValidateLogicalPathAcceptsRelative(t *testing.T) {
	ok := []string{"pool/main/a.deb", "simple/requests/requests-2.0.tar.gz", "packuments/left-pad.json"}
	for _, p := range ok {
		if err := ValidateLogicalPath(p); err != nil {
			t.Fatalf("%q should be valid: %v", p, err)
		}
	}
}

func TestValidateLogicalPathRejectsTraversal(t *testing.T) {
	bad := []string{
		"",
		"a\x00b",
		"/abs/path",
		"C:windows",
		`dir\file`,
		"..",
		"../secret",
		"foo/../bar",
		"foo/./bar",
		"foo/",
	}
	for _, p := range bad {
		if err := ValidateLogicalPath(p); err == nil {
			t.Fatalf("%q should be rejected", p)
		}
	}
}

func TestIsStagingLogical(t *testing.T) {
	yes := []string{".airgap-staging", ".airgap-staging/epoch", ".airgap-staging/epoch/unit/file"}
	for _, p := range yes {
		if !IsStagingLogical(p) {
			t.Fatalf("%q should be staging", p)
		}
	}
	no := []string{"pool/main/a.deb", "airgap-staging/file", "dists/.airgap-staging/x"}
	for _, p := range no {
		if IsStagingLogical(p) {
			t.Fatalf("%q should not be staging", p)
		}
	}
}
