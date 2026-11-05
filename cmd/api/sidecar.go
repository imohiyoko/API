package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/runtime"
	"github.com/imohiyoko/API/sidecar"
)

// sidecarMain は `api sidecar`。公開範囲ごとの口（REST と gRPC）を開き、操作を本体への HTTP（JSON）の呼び出しにする。
// 認証・認可は IAM_PUBLIC_URL などがあれば iam（adapters/iam.ConfigFromEnv）、-dev なら開発用のトークン。
func sidecarMain(m *manifest.Manifest, args []string) int {
	fs := flag.NewFlagSet("sidecar", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "本体の URL（例：http://127.0.0.1:3000）")
	listen := fs.String("listen", "", "REST の口：<公開範囲>=<アドレス> をカンマでつなぐ（例：internal=127.0.0.1:8090,public=:8092）")
	grpcListen := fs.String("grpc", "", "gRPC の口：-listen と同じ形")
	dev := fs.Bool("dev", false, "iam の代わりに開発用のトークン（dev-internal・dev-partner・dev-user）を受ける。本番では使わない")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest, err := parseListen(*listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	grpcAddrs, err := parseListen(*grpcListen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(rest) == 0 && len(grpcAddrs) == 0 {
		fmt.Fprintln(os.Stderr, "sidecar: give -listen or -grpc")
		return 2
	}
	// 本体への秘密は環境変数で渡す（コマンドラインはほかの利用者から見える）。
	reg, err := sidecar.Registry(m, sidecar.Config{Upstream: *upstream, Token: os.Getenv("API_SIDECAR_UPSTREAM_TOKEN")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg := runtime.Config{Audiences: map[string]runtime.AudienceConfig{}}
	if ic, ok := iam.ConfigFromEnv(); ok {
		if *dev {
			fmt.Fprintln(os.Stderr, "sidecar: -dev and IAM_PUBLIC_URL cannot be used together")
			return 2
		}
		c, err := iam.New(ic)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfg.Authenticator, cfg.Authorizer = c, c
		for _, aud := range m.AudienceList() {
			if r := iam.ResourceFromEnv(aud); r != "" {
				ac := runtime.DefaultAudienceConfig(aud)
				ac.Resource = r
				cfg.Audiences[aud] = ac
			}
		}
	} else if *dev {
		var actions []string
		for _, op := range m.Operations {
			if op.Authz != nil {
				actions = append(actions, op.Authz.Action)
			}
		}
		cfg.Authenticator, cfg.Authorizer = memory.DevTokens(actions...)
		log.Print("sidecar: using development tokens")
	} else {
		fmt.Fprintln(os.Stderr, "sidecar: set IAM_PUBLIC_URL (and the other IAM_* variables) or give -dev")
		return 2
	}
	srv, err := runtime.Build(reg, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, w := range srv.Warnings() {
		log.Printf("sidecar: warning: %s", w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, len(rest)+len(grpcAddrs))
	for aud := range rest {
		// 知らない公開範囲の口は、すべての要求に 404 を返すだけになる。起動の前に止める（gRPC は GRPCServer が拒む）。
		if m.Rank(aud) < 0 {
			fmt.Fprintf(os.Stderr, "sidecar: unknown audience %q in -listen\n", aud)
			return 2
		}
	}
	for aud, addr := range rest {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		s := &http.Server{
			Handler:           srv.Handler(aud),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		log.Printf("sidecar: %s REST on %s", aud, lis.Addr())
		go func() {
			if err := s.Serve(lis); err != http.ErrServerClosed {
				errs <- err
			}
		}()
		go func() { <-ctx.Done(); _ = s.Close() }()
	}
	for aud, addr := range grpcAddrs {
		gs, err := srv.GRPCServer(aud)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		log.Printf("sidecar: %s gRPC on %s", aud, lis.Addr())
		go func() {
			if err := gs.Serve(lis); err != nil {
				errs <- err
			}
		}()
		go func() { <-ctx.Done(); gs.Stop() }()
	}
	log.Print("sidecar: ready")
	select {
	case <-ctx.Done():
		return 0
	case err := <-errs:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
}

// parseListen は「公開範囲=アドレス」のカンマ区切りを読む。
func parseListen(s string) (map[string]string, error) {
	out := map[string]string{}
	if s == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		aud, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || aud == "" || addr == "" {
			return nil, fmt.Errorf("sidecar: %q must be <audience>=<address>", part)
		}
		if _, dup := out[aud]; dup {
			return nil, fmt.Errorf("sidecar: %s is given twice", aud)
		}
		out[aud] = addr
	}
	return out, nil
}
