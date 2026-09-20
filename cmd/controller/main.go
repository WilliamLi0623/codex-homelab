package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/WilliamLi0623/codex-homelab/internal/api"
	"github.com/WilliamLi0623/codex-homelab/internal/capacity"
	"github.com/WilliamLi0623/codex-homelab/internal/executor/k3s"
	"github.com/WilliamLi0623/codex-homelab/internal/orchestrator"
	"github.com/WilliamLi0623/codex-homelab/internal/store"
)

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	databasePath := flag.String("database", "controller.sqlite", "SQLite database path")
	flag.Parse()

	handler, closeStore, err := newHandlerFromEnvironment(*databasePath)
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
	return newHandlerWithDispatcher(databasePath, nil)
}

func newHandlerFromEnvironment(databasePath string) (http.Handler, func(), error) {
	database, err := store.Open(databasePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open controller store: %w", err)
	}
	closeStore := func() {
		if err := database.Close(); err != nil {
			log.Printf("close controller store: %v", err)
		}
	}

	config, configErr := loadEnvironmentConfig()
	if configErr != nil {
		return api.NewServerWithDispatcher(database, nil), closeStore, nil
	}

	proxmoxRuntime := capacity.NewProxmoxRuntime(config.Proxmox)
	manager := capacity.NewManager(proxmoxRuntime, capacity.VMIDRange{Min: controllerVMIDMin, Max: controllerVMIDMax})
	capacityAdapter := orchestrator.NewCapacityAdapterWithConfig(database, manager, config.CapacityConfig)
	kubernetesRuntime := k3s.NewKubernetesRuntime(config.Kubernetes)
	executor := k3s.New(kubernetesRuntime, database)
	dispatcher := orchestrator.New(capacityAdapter, executor)
	return api.NewServerWithDispatcher(database, dispatcher), closeStore, nil
}

// newHandlerWithDispatcher is the explicit assembly seam for production
// dependencies. A nil dispatcher is intentionally retained as an unconfigured
// state so readiness stays fail-closed until a validated broker is injected.
func newHandlerWithDispatcher(databasePath string, dispatcher api.Dispatcher) (http.Handler, func(), error) {
	database, err := store.Open(databasePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open controller store: %w", err)
	}
	return api.NewServerWithDispatcher(database, dispatcher), func() {
		if err := database.Close(); err != nil {
			log.Printf("close controller store: %v", err)
		}
	}, nil
}
