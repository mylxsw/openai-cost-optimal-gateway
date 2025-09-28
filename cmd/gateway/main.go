package main

import (
	"context"
	"flag"
	"os/signal"
	"syscall"
	"time"

	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/gateway"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/server"
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
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

	var usageStore storage.Store
	if cfg.SaveUsage {
		usageStore, err = storage.New(context.Background(), cfg.StorageType, cfg.StorageURI)
		if err != nil {
			log.Errorf("init usage storage: %v", err)
			return
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if cerr := usageStore.Close(ctx); cerr != nil {
				log.Warningf("close usage storage: %v", cerr)
			}
		}()
	}

	gw, err := gateway.New(cfg, usageStore)
	if err != nil {
		log.Errorf("init gateway: %v", err)
		return
	}

	srv := server.New(cfg, gw, usageStore)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Errorf("server exited with error: %v", err)
		return
	}
}
