// Command users は例のサーバー。check・manifest・gen は契約ファイルを書き出す（cli.Main）。
//
//	go run ./examples/users/cmd/users gen -out examples/users/api
//	go run ./examples/users/cmd/users serve
//
// serve は、IAM_PUBLIC_URL などがあれば iam を使い、無ければ開発用のトークン（dev-internal・dev-partner・dev-user）を受ける。
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

func main() {
	reg := users.Registry(&users.Store{})
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		os.Exit(cli.Main(reg, os.Args[1:]))
	}
	cfg := runtime.Config{}
	if url := os.Getenv("IAM_PUBLIC_URL"); url != "" {
		c, err := iam.New(iam.Config{
			PublicURL:    url,
			AdminURL:     os.Getenv("IAM_ADMIN_URL"),
			ClientID:     os.Getenv("IAM_CLIENT_ID"),
			ClientSecret: os.Getenv("IAM_CLIENT_SECRET"),
			AdminToken:   os.Getenv("IAM_ADMIN_TOKEN"),
		})
		if err != nil {
			log.Fatal(err)
		}
		cfg.Authenticator, cfg.Authorizer = c, c
	} else {
		log.Print("IAM_PUBLIC_URL is not set: using development tokens")
		cfg.Authenticator = memory.NewAuthenticator().
			Add("dev-internal", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_internal"}).
			Add("dev-partner", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_partner", OrgID: "org_partner"}).
			Add("dev-user", port.Principal{Type: port.PrincipalUser, ID: "usr_dev"})
		authz := memory.NewAuthorizer()
		for _, id := range []string{"sa_internal", "sa_partner", "usr_dev"} {
			for _, a := range []string{"users:read", "users:list", "users:create", "users:delete"} {
				authz.Allow(memory.Grant{PrincipalID: id, Action: a})
			}
		}
		cfg.Authorizer = authz
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
	<-ctx.Done()
}
