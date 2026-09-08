package npm

// PackumentLogicalPath returns the repository-relative metadata path used by the
// npm adapter. The Gateway uses the same layout instead of reimplementing it.
func PackumentLogicalPath(name string) string {
	return packumentLogicalPath(name)
}
