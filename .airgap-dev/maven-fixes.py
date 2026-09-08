from pathlib import Path

central = Path("internal/adapters/maven/central.go")
s = central.read_text()
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
central.write_text(s.replace(old, new))

generic = Path("internal/adapters/maven/generic.go")
s = generic.read_text()
old = '''\ta := domain.Artifact{ID: id, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Operation: op, PublishUnitID: deterministicUnitID(epoch.ID, rec.PublishUnit), PackageKey: rec.PackageKey, Version: rec.Version, Metadata: rec.Metadata, Attributes: rec.Attributes}
\tif op == domain.ArtifactDelete {
\t\ta.Size = 0
\t\ta.SHA256 = emptySHA256
\t} else {
\t\tintegrity, err := integrityFromHex("sha256", rec.SHA256)
\t\tif err != nil {
\t\t\treturn out, false, err
\t\t}
\t\ta.Size = rec.Size
\t\ta.SHA256 = rec.SHA256
\t\ta.UpstreamURL = rec.URL
\t\ta.UpstreamIntegrity = integrity
\t}
\tout = genericPlanned{UnitKey: rec.PublishUnit, MetadataPath: rec.MetadataPath, Artifact: a}'''
new = '''\tartifact := domain.Artifact{ID: id, EpochID: epoch.ID, SourceID: req.Source.ID, LogicalPath: logical, Operation: op, PublishUnitID: deterministicUnitID(epoch.ID, rec.PublishUnit), PackageKey: rec.PackageKey, Version: rec.Version, Metadata: rec.Metadata, Attributes: rec.Attributes}
\tif op == domain.ArtifactDelete {
\t\tartifact.Size = 0
\t\tartifact.SHA256 = emptySHA256
\t} else {
\t\tintegrity, err := integrityFromHex("sha256", rec.SHA256)
\t\tif err != nil {
\t\t\treturn out, false, err
\t\t}
\t\tartifact.Size = rec.Size
\t\tartifact.SHA256 = rec.SHA256
\t\tartifact.UpstreamURL = rec.URL
\t\tartifact.UpstreamIntegrity = integrity
\t}
\tout = genericPlanned{UnitKey: rec.PublishUnit, MetadataPath: rec.MetadataPath, Artifact: artifact}'''
if old not in s:
    raise SystemExit("generic artifact source shape changed")
generic.write_text(s.replace(old, new))
