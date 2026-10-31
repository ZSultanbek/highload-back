// Command rest-server runs the REST/JSON baseline (HTTP/1.1).
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/ZSultanbek/highload-back/sis2/internal/restserver"
	"github.com/ZSultanbek/highload-back/sis2/internal/service"
	"github.com/ZSultanbek/highload-back/sis2/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	failRate := flag.Float64("fail-rate", 0, "inject transient failures with this probability [0..1]")
	flag.Parse()

	svc := service.NewWithFailRate(store.New(), *failRate)
	srv := restserver.New(svc)

	log.Printf("REST ImageService listening on %s (fail-rate=%.2f)", *addr, *failRate)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
