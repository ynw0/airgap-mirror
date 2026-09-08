package ports

import (
	"context"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type InventoryProgress struct {
	ScannedObjects int64 `json:"scannedObjects"`
	ScannedBytes   int64 `json:"scannedBytes"`
}

type InventoryRequest struct {
	Source  domain.Source
	Current CatalogStore
	Report  func(InventoryProgress) error
}

type InventoryAdapter interface {
	Inventory(context.Context, InventoryRequest, CatalogBuildSink) (domain.CatalogStats, error)
}
