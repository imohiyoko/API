// Command users-rest は、REST だけを使う例のサーバー（gRPC・YAML を使わない利用者のビルド）。
// runtime/grpcx・manifest/yamlx・cli を import しないので、ビルドに grpc・protobuf・yaml が入らない
// （internal/archtest の TestRESTOnlyBuildHasNoGRPCOrYAML が確かめる）。
//
//	go run ./examples/users/cmd/users-rest   # 開発用のトークン dev-internal・dev-partner・dev-user
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/runtime"
)

func main() {
	authn, authz := memory.DevTokens("users:read", "users:list", "users:create", "users:delete")
	srv, err := runtime.Build(users.Registry(&users.Store{}), runtime.Config{Authenticator: authn, Authorizer: authz})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// 公開範囲ごとに別の listener（design.md 4.1）。internal は社内網だけに出す。
	addrs := map[string]string{"internal": "127.0.0.1:8090", "partner": ":8091", "public": ":8092"}
	for aud, addr := range addrs {
		s := &http.Server{Addr: addr, Handler: srv.Handler(aud), ReadHeaderTimeout: 5 * time.Second}
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
