package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/gateway"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	gw, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("init gateway: %v", err)
	}

	srv := server.New(cfg, gw)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server exited with error: %v", err)
	}
}
