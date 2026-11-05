// Command users は例のサーバー。check・manifest・gen は契約ファイルを書き出す（cli.Main）。
//
//	go run ./examples/users/cmd/users gen -out examples/users/api
//	go run ./examples/users/cmd/users serve
//
// serve は、IAM_PUBLIC_URL などがあれば iam を使い、無ければ開発用のトークン（dev-internal・dev-partner・dev-user）を受ける。
// IAM_RESOURCE_INTERNAL・IAM_RESOURCE_PARTNER・IAM_RESOURCE_PUBLIC があれば、その口は aud がその URI のトークンだけを受ける
// （iam に登録したリソース。RFC 8707）。
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/runtime"
)

func main() {
	reg := users.Registry(&users.Store{})
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		os.Exit(cli.Main(reg, os.Args[1:]))
	}
	cfg := runtime.Config{}
	if ic, ok := iam.ConfigFromEnv(); ok {
		c, err := iam.New(ic)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Authenticator, cfg.Authorizer = c, c
		cfg.Audiences = map[string]runtime.AudienceConfig{}
		for _, aud := range []string{"internal", "partner", "public"} {
			if r := iam.ResourceFromEnv(aud); r != "" {
				ac := runtime.DefaultAudienceConfig(aud)
				ac.Resource = r
				cfg.Audiences[aud] = ac
			}
		}
	} else {
		log.Print("IAM_PUBLIC_URL is not set: using development tokens")
		cfg.Authenticator, cfg.Authorizer = memory.DevTokens("users:read", "users:list", "users:create", "users:delete")
	}
	srv, err := runtime.Build(reg, cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// 公開範囲ごとに別の listener（design.md 4.1）。internal は社内網だけに出す。
	addrs := map[string]string{"internal": "127.0.0.1:8090", "partner": ":8091", "public": ":8092"}
	for aud, addr := range addrs {
		s := &http.Server{
			Addr:              addr,
			Handler:           srv.Handler(aud),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Printf("%s API on %s", aud, addr)
			if err := s.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatal(err)
			}
		}()
		go func() { <-ctx.Done(); _ = s.Close() }()
	}
	// gRPC の口（design.md 5.2）。gRPC で出す操作がある公開範囲だけ。internal はリフレクションも載る。
	grpcAddrs := map[string]string{"internal": "127.0.0.1:9090", "partner": ":9091", "public": ":9092"}
	for aud, addr := range grpcAddrs {
		gs, err := srv.GRPCServer(aud)
		if err != nil {
			continue
		}
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			log.Printf("%s gRPC on %s", aud, addr)
			if err := gs.Serve(lis); err != nil {
				log.Fatal(err)
			}
		}()
		go func() { <-ctx.Done(); gs.Stop() }()
	}
	<-ctx.Done()
}
