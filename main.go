package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"wowinsight/internal/env"
	"wowinsight/internal/warcraftlogs"
)

const addr = ":8080"

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run is main's body with an error return, so that every deferred function
// above it still runs. log.Fatal and a bare os.Exit run none of them.
func run() error {
	if err := env.Load(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	tpl, err := parseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	s := newServer(
		warcraftlogs.New(
			env.First("WARCRAFTLOGS_CLIENT_ID", "ClientId"),
			env.First("WARCRAFTLOGS_CLIENT_SECRET", "ClientSecret"),
		),
		tpl,
		log.Default(),
	)

	log.Printf("listening on %s", addr)
	return http.ListenAndServe(addr, s.routes())
}
