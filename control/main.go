package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

var appCtx = context.Background()

func main() {
	databaseURL := getenv("DATABASE_URL", "postgres://atlas:atlas@localhost:5432/atlas?sslmode=disable")
	redisURL := getenv("REDIS_URL", "redis://localhost:6379/0")
	natsURL := getenv("NATS_URL", "nats://localhost:4222")

	store, err := waitForPostgres(appCtx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	var nc *nats.Conn
	for i := 0; i < 30; i++ {
		nc, err = nats.Connect(natsURL)
		if err == nil {
			break
		}
		log.Printf("waiting for nats: %v", err)
		time.Sleep(1 * time.Second)
	}
	if nc == nil {
		log.Fatal("failed to connect to nats")
	}
	defer nc.Close()

	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Printf("redis parse warning: %v (continuing without cache)", err)
	}

	var redisClient *redis.Client
	if err == nil {
		redisClient = redis.NewClient(redisOpts)
		if err := redisClient.Ping(appCtx).Err(); err != nil {
			log.Printf("redis unavailable: %v (continuing without cache)", err)
			redisClient = nil
		}
	}

	server := &Server{store: store, nc: nc, redis: redisClient}
	go server.startStatusReconciler(appCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "atlas-control"})
	})
	mux.HandleFunc("/campaigns", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			server.createCampaign(w, r)
		case http.MethodGet:
			server.listCampaigns(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/campaigns/", campaignHandler(server))
	mux.HandleFunc("/domains", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			server.seedDomains(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/domains/", domainHandler(server))
	mux.HandleFunc("/pivots/", pivotHandler(server))
	mux.HandleFunc("/ct/config", ctHandler(server))
	mux.HandleFunc("/ct/backfill", ctHandler(server))
	mux.HandleFunc("/ct/status", ctHandler(server))
	mux.HandleFunc("/metrics", metricsHandler(server))
	mux.HandleFunc("/metrics/prometheus", metricsHandler(server))

	addr := getenv("LISTEN_ADDR", ":8090")
	log.Printf("atlas control-api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
