package apt

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

// Keyrings are snapshots of the official ubuntu-keyring and
// debian-archive-keyring packages, not keys fetched from the selected mirror.
//
//go:embed keyrings/ubuntu.b64
var ubuntuArchiveKeys string

//go:embed keyrings/debian.b64
var debianArchiveKeys string

// archiveKeyring selects the distribution from the configured suite, not from
// the mirror's response. An existing explicit keyring takes precedence.
func archiveKeyring(cfg Config) ([]byte, error) {
	if strings.TrimSpace(cfg.TrustedKeyringBase64) != "" {
		return decodeTrustedKeyring(cfg.TrustedKeyringBase64)
	}
	suite, _, _ := strings.Cut(strings.ToLower(cfg.Suite), "-")
	switch suite {
	case "focal", "jammy", "noble", "resolute":
		return decodeTrustedKeyring(ubuntuArchiveKeys)
	case "bullseye", "bookworm", "trixie", "forky", "sid", "stable", "oldstable", "oldoldstable", "testing", "unstable":
		return decodeTrustedKeyring(debianArchiveKeys)
	default:
		return nil, fmt.Errorf("暂未内置 %s 发行版的仓库签名公钥，请选择受支持的 Ubuntu 或 Debian 发行版: %w", cfg.Suite, domain.ErrInvalid)
	}
}
