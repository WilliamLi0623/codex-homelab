package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/WilliamLi0623/codex-homelab/internal/api"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	databasePath := flag.String("database", "controller.sqlite", "SQLite database path")
	flag.Parse()

	handler, closeStore, err := newHandler(*databasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer closeStore()

	log.Printf("controller listening on %s", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, handler); err != nil {
		log.Fatal(err)
	}
}

func newHandler(databasePath string) (http.Handler, func(), error) {
	database, err := store.Open(databasePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open controller store: %w", err)
	}
	return api.NewServer(database), func() {
		if err := database.Close(); err != nil {
			log.Printf("close controller store: %v", err)
		}
	}, nil
}
