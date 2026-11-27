package apt

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

const maxTrustedKeyring = 1 << 20

func decodeTrustedKeyring(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("APT 尚未配置可信上游公钥环，请先配置 trustedKeyringBase64: %w", domain.ErrInvalid)
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxTrustedKeyring)+4096 {
		return nil, fmt.Errorf("APT 可信公钥环过大: %w", domain.ErrInvalid)
	}
	keys, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(keys) == 0 || len(keys) > maxTrustedKeyring {
		return nil, fmt.Errorf("APT 公钥环必须为 Base64 编码的二进制 OpenPGP 公钥环: %w", domain.ErrInvalid)
	}
	if err := validatePublicKeyring(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// Inspect packet framing before storing or exporting a configured keyring.
// Reject secret-key packets, compressed data, and other non-keyring objects.
// The OpenPGP library performs cryptographic parsing during verification.
func validatePublicKeyring(data []byte) error {
	bad := func() error {
		return fmt.Errorf("APT 公钥环必须是仅含公钥的二进制 OpenPGP 导出文件，不得包含私钥或其他数据: %w", domain.ErrInvalid)
	}
	primary := false
	for len(data) > 0 {
		header := data[0]
		data = data[1:]
		if header&0x80 == 0 {
			return bad()
		}
		var tag byte
		var n uint64
		if header&0x40 != 0 {
			tag = header & 0x3f
			if len(data) == 0 {
				return bad()
			}
			first := data[0]
			data = data[1:]
			switch {
			case first < 192:
				n = uint64(first)
			case first < 224:
				if len(data) < 1 {
					return bad()
				}
				n = uint64(first-192)*256 + uint64(data[0]) + 192
				data = data[1:]
			case first == 255:
				if len(data) < 4 {
					return bad()
				}
				n = uint64(binary.BigEndian.Uint32(data[:4]))
				data = data[4:]
			default:
				return bad()
			}
		} else {
			tag = (header >> 2) & 0x0f
			lengthBytes := 1 << (header & 3)
			if lengthBytes > 4 || len(data) < lengthBytes {
				return bad()
			}
			for _, v := range data[:lengthBytes] {
				n = n*256 + uint64(v)
			}
			data = data[lengthBytes:]
		}
		switch tag {
		case 6:
			primary = true
		case 2, 10, 12, 13, 14, 17:
			// Signature, marker, trust, user ID, public subkey, user attribute.
		default:
			return bad()
		}
		if n > uint64(len(data)) {
			return bad()
		}
		data = data[int(n):]
	}
	if !primary {
		return bad()
	}
	return nil
}

// verifyUpstreamRelease verifies all signature packets against the configured
// archive keys, without using a system keyring or external GnuPG executable.
func verifyUpstreamRelease(ctx context.Context, keys, release, signature []byte, inline bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keyring, err := openpgp.ReadKeyRing(bytes.NewReader(keys))
	if err != nil || len(keyring) == 0 {
		return nil, fmt.Errorf("无法读取 APT 仓库签名公钥: %w", domain.ErrInvalid)
	}
	plain, signed := release, release
	var signatures [][]byte
	if inline {
		if err = checkClearSignedEnvelope(release); err != nil {
			return nil, err
		}
		block, rest := clearsign.Decode(release)
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("无法读取 InRelease 签名: %w", domain.ErrInvalid)
		}
		plain, signed = block.Plaintext, block.Bytes
		data, err := io.ReadAll(block.ArmoredSignature.Body)
		if err != nil {
			return nil, err
		}
		signatures = append(signatures, data)
	} else {
		// Debian may publish multiple consecutive armored detached signatures.
		// Decode each complete block, or accept the binary signature stream.
		if bytes.HasPrefix(bytes.TrimSpace(signature), []byte("-----BEGIN PGP SIGNATURE-----")) {
			rest := bytes.TrimSpace(signature)
			marker := []byte("-----END PGP SIGNATURE-----")
			for len(rest) > 0 {
				if !bytes.HasPrefix(rest, []byte("-----BEGIN PGP SIGNATURE-----")) {
					return nil, domain.ErrInvalid
				}
				i := bytes.Index(rest, marker)
				if i < 0 {
					return nil, domain.ErrInvalid
				}
				end := i + len(marker)
				block, err := armor.Decode(bytes.NewReader(rest[:end]))
				if err != nil || block.Type != openpgp.SignatureType {
					return nil, domain.ErrInvalid
				}
				data, err := io.ReadAll(block.Body)
				if err != nil {
					return nil, err
				}
				signatures = append(signatures, data)
				rest = bytes.TrimSpace(rest[end:])
			}
		} else {
			signatures = append(signatures, signature)
		}
	}
	now := time.Now()
	config := &packet.Config{Time: func() time.Time { return now }}
	count := 0
	for _, data := range signatures {
		reader := bytes.NewReader(data)
		for reader.Len() > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			p, err := packet.Read(reader)
			sig, ok := p.(*packet.Signature)
			if err != nil || !ok || (sig.SigType != packet.SigTypeBinary && sig.SigType != packet.SigTypeText) {
				return nil, fmt.Errorf("APT 签名格式无效: %w", domain.ErrConflict)
			}
			if sig.CreationTime.After(now.Add(10 * time.Minute)) {
				return nil, fmt.Errorf("APT 签名时间超出允许范围，请检查系统时间: %w", domain.ErrConflict)
			}
			var encoded bytes.Buffer
			if err = sig.Serialize(&encoded); err != nil {
				return nil, err
			}
			if _, err = openpgp.CheckDetachedSignatureAndHash(keyring, bytes.NewReader(signed), &encoded, []crypto.Hash{crypto.SHA256, crypto.SHA384, crypto.SHA512}, config); err != nil {
				return nil, fmt.Errorf("APT 仓库签名验证失败，请检查镜像站、签名有效期及系统时间: %v: %w", err, domain.ErrConflict)
			}
			count++
		}
	}
	if count == 0 || len(plain) == 0 || len(plain) > 32<<20 {
		return nil, fmt.Errorf("未找到有效的 APT 仓库签名: %w", domain.ErrConflict)
	}
	return plain, nil
}

func checkClearSignedEnvelope(body []byte) error {
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "-----BEGIN PGP SIGNED MESSAGE-----\n") ||
		strings.Count(text, "-----BEGIN PGP SIGNED MESSAGE-----") != 1 ||
		strings.Count(text, "-----BEGIN PGP SIGNATURE-----") != 1 ||
		strings.Count(text, "-----END PGP SIGNATURE-----") != 1 ||
		!strings.HasSuffix(strings.TrimRight(text, "\n"), "\n-----END PGP SIGNATURE-----") {
		return fmt.Errorf("InRelease 必须是单一完整的 OpenPGP 明文签名消息: %w", domain.ErrInvalid)
	}
	return nil
}

func validateGPGStatus(status string, now time.Time) error {
	valid := 0
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "[GNUPG:]" {
			continue
		}
		switch fields[1] {
		case "BADSIG", "ERRSIG", "NO_PUBKEY", "EXPKEYSIG", "EXPSIG", "REVKEYSIG", "KEYEXPIRED", "SIGEXPIRED", "NODATA", "FAILURE", "ERROR":
			return fmt.Errorf("APT 签名未通过可信验证（%s）: %w", fields[1], domain.ErrConflict)
		case "VALIDSIG":
			// VALIDSIG fingerprint date timestamp expires version reserved
			// pubkey-algorithm hash-algorithm signature-class [primary-fpr].
			if len(fields) < 11 || (fields[9] != "8" && fields[9] != "9" && fields[9] != "10") {
				return fmt.Errorf("APT 签名必须使用 SHA256、SHA384 或 SHA512: %w", domain.ErrConflict)
			}
			if fields[10] != "00" && fields[10] != "01" {
				return fmt.Errorf("APT 签名类型不符合 Release 文件签名要求: %w", domain.ErrConflict)
			}
			signed, e := strconv.ParseInt(fields[4], 10, 64)
			expires, expErr := strconv.ParseInt(fields[5], 10, 64)
			if e != nil || expErr != nil || signed <= 0 || expires < 0 ||
				time.Unix(signed, 0).After(now.Add(10*time.Minute)) ||
				(expires != 0 && !time.Unix(expires, 0).After(now)) {
				return fmt.Errorf("APT 签名时间无效或已过期: %w", domain.ErrConflict)
			}
			valid++
		}
	}
	if valid == 0 {
		return fmt.Errorf("未发现有效且可信的 APT 上游签名: %w", domain.ErrConflict)
	}
	return nil
}

// Freshness checks apply to the verified plaintext, before parsing any
// package or index checksums. Stable archives may legitimately omit Valid-Until.
func validateReleaseFreshness(body []byte, cfg Config, now time.Time) error {
	s := bufio.NewScanner(bytes.NewReader(body))
	s.Buffer(make([]byte, 64<<10), 2<<20)
	fields := make(map[string]string)
	for s.Scan() {
		line := s.Text()
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		switch key {
		case "date", "valid-until", "suite", "codename":
			if _, exists := fields[key]; exists {
				return fmt.Errorf("APT Release 字段重复（%s）: %w", key, domain.ErrConflict)
			}
			fields[key] = strings.TrimSpace(value)
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	if fields["suite"] != cfg.Suite && fields["codename"] != cfg.Suite {
		return fmt.Errorf("已签名的 Release Suite/Codename 与配置的 %s 不一致: %w", cfg.Suite, domain.ErrConflict)
	}
	date, err := mail.ParseDate(fields["date"])
	if err != nil || date.After(now.Add(10*time.Minute)) {
		return fmt.Errorf("已签名的 Release Date 无效或超出允许的未来时间: %w", domain.ErrConflict)
	}
	if value, present := fields["valid-until"]; present {
		until, err := mail.ParseDate(value)
		if err != nil || !until.After(date) || !until.After(now) {
			return fmt.Errorf("已签名的 Release Valid-Until 无效或已过期: %w", domain.ErrConflict)
		}
	} else if cfg.RequireValidUntil {
		return fmt.Errorf("此软件源要求 Release 必须包含 Valid-Until: %w", domain.ErrConflict)
	}
	return nil
}
