package ports

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type CatalogMembership interface {
	Contains(context.Context, string, string) (bool, error)
}

type ManagedPathMatcher func(string) (bool, error)

type ManagedPathAdapter interface {
	ManagedPaths(context.Context, domain.Source) (ManagedPathMatcher, error)
}

type GCCandidateSink interface {
	Put(context.Context, domain.GCCandidate) error
	Stats(context.Context) (domain.GCStats, error)
	Path() string
	Close() error
	Abort() error
}

type GCCandidateReader interface {
	Stats(context.Context) (domain.GCStats, error)
	List(context.Context, string, int) ([]domain.GCCandidate, error)
	Walk(context.Context, func(domain.GCCandidate) error) error
	Close() error
}

type GCCandidateFactory interface {
	Create(context.Context, string) (GCCandidateSink, error)
	Open(context.Context, string) (GCCandidateReader, error)
}
