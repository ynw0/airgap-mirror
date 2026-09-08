from pathlib import Path

path = Path('internal/service/downloader.go')
source = path.read_text(encoding='utf-8')
for line in [
    '\t"crypto/sha1"\n',
    '\t"crypto/sha256"\n',
    '\t"crypto/sha512"\n',
    '\t"encoding/base64"\n',
    '\t"encoding/hex"\n',
    '\t"hash"\n',
]:
    if line not in source:
        raise SystemExit(f'missing downloader import: {line.strip()}')
    source = source.replace(line, '', 1)

marker = '\t"github.com/ynw0/airgap-mirror/internal/domain"\n'
if marker not in source:
    raise SystemExit('downloader domain import marker changed')
source = source.replace(marker, marker + '\t"github.com/ynw0/airgap-mirror/internal/integrity"\n', 1)

start = source.find('\nfunc verifyFile(')
if start < 0:
    raise SystemExit('downloader verifyFile function not found')
source = source[:start] + '''\nfunc verifyFile(path, upstreamIntegrity string) (string, int64, error) {\n\treturn integrity.VerifyFile(path, upstreamIntegrity)\n}\n'''
path.write_text(source, encoding='utf-8')
