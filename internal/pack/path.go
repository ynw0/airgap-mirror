package pack

import (
	"fmt"
	"path"
	"strings"
)

func ValidateLogicalPath(p string) error {
	if p == "" || strings.ContainsRune(p, '\x00') {
		return fmt.Errorf("invalid logical path")
	}
	if strings.Contains(p, "\\") || strings.HasPrefix(p, "/") || len(p) >= 2 && p[1] == ':' {
		return fmt.Errorf("unsafe logical path %q", p)
	}
	c := path.Clean(p)
	if c == "." || c != p || c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("unsafe logical path %q", p)
	}
	return nil
}

// IsStagingLogical 判断路径是否落在仓库内的 metadata 暂存树。
func IsStagingLogical(p string) bool {
	c := path.Clean(p)
	return c == ".airgap-staging" || strings.HasPrefix(c, ".airgap-staging/")
}
