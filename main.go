// Command wowinsight is the shipped binary: the HTTP layer over a Warcraft
// Logs client with real credentials. The development binaries under cmd/dev
// compose the same HTTP layer over other things.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"wowinsight/internal/env"
	"wowinsight/internal/warcraftlogs"
	"wowinsight/internal/web"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// run is main's body with an error return, so that every deferred function
// above it still runs. log.Fatal and a bare os.Exit run none of them.
func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("wowinsight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8080", "address to listen on")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if err := env.Load(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}
	tpl, err := web.ParseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	s := web.New(
		warcraftlogs.New(
			env.First("WARCRAFTLOGS_CLIENT_ID", "ClientId"),
			env.First("WARCRAFTLOGS_CLIENT_SECRET", "ClientSecret"),
		),
		tpl,
		log.Default(),
	)

	log.Printf("listening on %s", *addr)
	return http.ListenAndServe(*addr, s.Routes())
}
