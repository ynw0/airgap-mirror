package service

import (
	"net/http"

	"github.com/ynw0/airgap-mirror/internal/adapters/apt"
	"github.com/ynw0/airgap-mirror/internal/adapters/maven"
	"github.com/ynw0/airgap-mirror/internal/adapters/npm"
	"github.com/ynw0/airgap-mirror/internal/adapters/pypi"
)

// NewDefaultRegistry is the single registration point shared by Agent and desktop client.
func NewDefaultRegistry(client *http.Client) (*Registry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return NewRegistry(
		apt.New(client),
		pypi.New(client),
		npm.New(client),
		maven.NewGeneric(client),
		maven.NewCentral(client),
	)
}
