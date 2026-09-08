package domain

type GCCandidate struct {
	LogicalPath     string `json:"logicalPath"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
}

type GCStats struct {
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
}
