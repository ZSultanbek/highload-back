// Command grpc-server runs the ImageService over gRPC (HTTP/2 + protobuf).
package main

import (
	"flag"
	"log"
	"net"

	imagev1 "github.com/ZSultanbek/highload-back/sis2/gen/image/v1"
	"github.com/ZSultanbek/highload-back/sis2/internal/grpcserver"
	"github.com/ZSultanbek/highload-back/sis2/internal/service"
	"github.com/ZSultanbek/highload-back/sis2/internal/store"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8081", "listen address")
	failRate := flag.Float64("fail-rate", 0, "inject transient failures with this probability [0..1]")
	flag.Parse()

	svc := service.NewWithFailRate(store.New(), *failRate)
	srv := grpc.NewServer()
	imagev1.RegisterImageServiceServer(srv, grpcserver.New(svc))

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("gRPC ImageService listening on %s (fail-rate=%.2f)", *addr, *failRate)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
