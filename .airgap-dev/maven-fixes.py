from pathlib import Path

path = Path("internal/adapters/maven/central.go")
s = path.read_text()
s = s.replace('\t"net/url"\n', '')
old = '''func centralURL(base, logical string) (string, error) {
\troot, err := url.Parse(strings.TrimRight(base, "/") + "/")
\tif err != nil {
\t\treturn "", err
\t}
\tparts := strings.Split(logical, "/")
\tfor i := range parts {
\t\tparts[i] = url.PathEscape(parts[i])
\t}
\treturn root.ResolveReference(&url.URL{Path: strings.Join(parts, "/")}).String(), nil
}'''
new = '''func centralURL(base, logical string) (string, error) {
\tif _, err := cleanLogicalPath(logical); err != nil {
\t\treturn "", err
\t}
\t// Central coordinates are validated to a URL-path-safe subset, so joining the
\t// canonical logical path directly avoids double-encoding already escaped bytes.
\treturn strings.TrimRight(base, "/") + "/" + logical, nil
}'''
if old not in s:
    raise SystemExit("centralURL source shape changed")
path.write_text(s.replace(old, new))
