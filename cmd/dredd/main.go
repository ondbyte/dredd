// Command dredd is the API server + queue worker for the dredd code
// execution service.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/ondbyte/dredd"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/langs"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	reg, err := langs.Load(cfg.LanguagesFile, cfg.RootfsDir)
	if err != nil {
		log.Fatalf("load languages: %v", err)
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	app, err := dredd.New(dredd.Options{Config: cfg, Registry: reg, Redis: rdb})
	if err != nil {
		log.Fatalf("dredd: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("dredd listening on %s", app.Addr())
	go func() {
		if err := app.Run(ctx); err != nil {
			log.Fatalf("dredd: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = app.Shutdown(shutdownCtx)
}
