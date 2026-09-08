package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	appserver "github.com/ynw0/airgap-mirror/internal/server"
	"github.com/ynw0/airgap-mirror/internal/service"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func main() {
	var listen, dbPath, staging, exports, token string
	flag.StringVar(&listen, "listen", "127.0.0.1:8787", "HTTP listen address")
	flag.StringVar(&dbPath, "db", "/var/lib/airgap-mirror/server.db", "server SQLite database")
	flag.StringVar(&staging, "staging", "/var/lib/airgap-mirror/transfer-staging", "resumable upload staging directory")
	flag.StringVar(&exports, "exports", "/var/lib/airgap-mirror/state-exports", "State Capsule export directory")
	flag.StringVar(&token, "token", os.Getenv("AIRGAP_MIRROR_TOKEN"), "Bearer token; AIRGAP_MIRROR_TOKEN is preferred")
	flag.Parse()
	if token == "" {
		log.Fatal("bearer token is required via -token or AIRGAP_MIRROR_TOKEN")
	}
	for _, p := range []string{filepath.Dir(dbPath), staging, exports} {
		if err := os.MkdirAll(p, 0750); err != nil {
			log.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		cancel()
		log.Fatal(err)
	}
	cancel()
	store := storesqlite.NewServerStore(db)
	registry, err := service.NewRegistry()
	if err != nil {
		log.Fatal(err)
	}
	publisher := service.PublisherService{Store: store, Catalog: store, Registry: registry}
	importer := service.ImportService{Store: store, Installer: service.FileInstaller{}, Manifest: service.ManifestReader{Open: func(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }}, Publisher: publisher}
	capacity := service.FSCapacityInspector{}
	api := &appserver.API{Store: store, Catalog: store, Registrar: service.BundleRegistrar{Store: store, Capacity: capacity, StagingRoot: staging}, Transfer: service.TransferService{Store: store}, Importer: importer, Capsules: service.CapsuleService{Store: store, Exporter: store, OutputDir: exports}, Capacity: capacity, BearerToken: token}
	httpServer := &http.Server{Addr: listen, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, WriteTimeout: 0}
	errCh := make(chan error, 1)
	go func() { log.Printf("mirror-agent listening on %s", listen); errCh <- httpServer.ListenAndServe() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("received %s", s)
	case e := <-errCh:
		if e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err = httpServer.Shutdown(shutdown); err != nil {
		fmt.Fprintln(os.Stderr, "shutdown:", err)
	}
}
