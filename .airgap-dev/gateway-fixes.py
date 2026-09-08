from pathlib import Path

api = Path("internal/server/api.go")
s = api.read_text()
needle = '\tmux.HandleFunc("GET /api/v1/health", a.health)\n'
insert = needle + '\t// npm metadata is a repository data-plane endpoint; package clients do not use the Agent bearer token.\n\tmux.HandleFunc("GET /npm/{sourceID}/-/ping", a.npmPing)\n\tmux.HandleFunc("GET /npm/{sourceID}/{package...}", a.npmPackument)\n'
if needle not in s:
    raise SystemExit("API route anchor changed")
if 'GET /npm/{sourceID}/{package...}' not in s:
    s = s.replace(needle, insert, 1)
api.write_text(s)

main = Path("cmd/mirror-agent/main.go")
s = main.read_text()
old = 'api := &appserver.API{Store: store, Catalog: store, Registrar:'
new = 'api := &appserver.API{Store: store, Catalog: store, Registry: registry, Registrar:'
if old not in s:
    if new not in s:
        raise SystemExit("mirror-agent API wiring anchor changed")
else:
    s = s.replace(old, new, 1)
main.write_text(s)
