// Command waggled runs the Waggle stigmergic coordination substrate: a shared
// field where agent swarms coordinate indirectly through decaying signals,
// leases, broadcasts and durable memory.
//
// Usage:
//
//	waggled -addr :7777 -data ./waggle-data
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":7777", "listen address")
	dataDir := flag.String("data", "", "journal directory (empty = in-memory only)")
	debug := flag.Bool("debug", false, "enable /v1/debug/attack-metrics for red-team scoring (off in production)")
	tabooKey := flag.String("taboo-auth-key", "", "Èṣù ed25519 public key (hex) — verify taboo-capability tokens; taboo deposits get taboo_authenticated set")
	tabooEnforce := flag.Bool("taboo-auth-enforce", false, "reject taboo deposits without a valid capability (requires -taboo-auth-key)")
	flag.Parse()

	store, err := OpenStore(*dataDir)
	if err != nil {
		log.Fatalf("waggled: open store: %v", err)
	}
	defer store.Close()

	srv := NewServer(store)
	if *debug {
		srv.EnableDebug()
		log.Printf("waggled: -debug on — attack metrics at /v1/debug/attack-metrics")
	}
	if *tabooKey != "" {
		if err := srv.EnableTabooAuth(*tabooKey, *tabooEnforce); err != nil {
			log.Fatalf("waggled: taboo-auth: %v", err)
		}
		mode := "observe"
		if *tabooEnforce {
			mode = "enforce"
		}
		log.Printf("waggled: Èṣù taboo-capability verification on (%s mode)", mode)
	} else if *tabooEnforce {
		log.Fatalf("waggled: -taboo-auth-enforce requires -taboo-auth-key")
	}
	if err := srv.replay(*dataDir); err != nil {
		log.Fatalf("waggled: replay journal: %v", err)
	}
	if n := srv.field.Sweep(); n > 0 {
		log.Printf("waggled: swept %d evaporated signals after replay", n)
	}

	// periodic evaporation sweep keeps the field small
	go func() {
		for range time.Tick(time.Minute) {
			srv.field.Sweep()
		}
	}()

	log.Printf("waggled: substrate listening on %s (manifest at /.well-known/waggle.json, observatory at /)", *addr)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatalf("waggled: %v", err)
	}
}
