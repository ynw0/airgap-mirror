from pathlib import Path

path = Path("internal/client/bundle_transfer.go")
s = path.read_text(encoding="utf-8")

s = s.replace('import (\n\t"context"\n\t"database/sql"', 'import (\n\t"context"\n\t"database/sql"\n\t"encoding/hex"', 1)
s = s.replace('spec.Size < pack.HeaderSize', 'spec.Size < pack.PackHeaderSize')
old = '''\t\tif _, err = strconv.ParseUint(spec.SHA256[:16], 16, 64); err != nil {\n\t\t\treturn out, fmt.Errorf("invalid pack sha256 for %s: %w", spec.ID, domain.ErrInvalid)\n\t\t}'''
new = '''\t\tdigest, digestErr := hex.DecodeString(spec.SHA256)\n\t\tif digestErr != nil || len(digest) != 32 {\n\t\t\treturn out, fmt.Errorf("invalid pack sha256 for %s: %w", spec.ID, domain.ErrInvalid)\n\t\t}'''
if old not in s:
    raise SystemExit("pack sha validation source shape changed")
s = s.replace(old, new, 1)
path.write_text(s, encoding="utf-8")
