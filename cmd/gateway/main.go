package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"

	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/gateway"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Errorf("load config: %v", err)
		return
	}

	// Initialize logging with debug configuration
	if cfg.Debug {
		log.DefaultWithFileLine(true)
		log.Debug("Debug logging enabled")
	}

	log.Infof("Starting OpenAI Cost Optimal Gateway on %s", cfg.Listen)

	gw, err := gateway.New(cfg)
	if err != nil {
		log.Errorf("init gateway: %v", err)
		return
	}

	srv := server.New(cfg, gw)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Errorf("server exited with error: %v", err)
		return
	}
}
