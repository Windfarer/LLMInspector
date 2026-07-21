package main

import (
	"flag"
	"log"
	"net/http"
	"path/filepath"

	"github.com/windfarer/llminspector/api"
	"github.com/windfarer/llminspector/metrics"
	"github.com/windfarer/llminspector/proxy"
	"github.com/windfarer/llminspector/store"
)

func main() {
	targetURL := flag.String("target", "https://api.openai.com", "Target upstream LLM serving address")
	proxyPort := flag.String("proxy-port", "8080", "Port for the reverse proxy")
	apiPort := flag.String("api-port", "8081", "Port for the dashboard API (WebSocket)")
	userHeader := flag.String("user-header", "X-User-ID", "HTTP header used to identify the user")
	dbPath := flag.String("db", "metrics.db", "Path to SQLite database")
	useTiktoken := flag.Bool("tiktoken", true, "Use tiktoken for precise counting (slower but accurate)")

	flag.Parse()

	log.Printf("Starting LLM Inspector")
	log.Printf("Target: %s", *targetURL)

	db, err := store.NewStore(filepath.Clean(*dbPath))
	if err != nil {
		log.Fatalf("Failed to init db: %v", err)
	}

	manager := metrics.NewManager(db, *useTiktoken)
	apiServer := api.NewServer(manager)

	proxyServer, err := proxy.NewProxyServer(*targetURL, *userHeader, manager)
	if err != nil {
		log.Fatalf("Failed to init proxy: %v", err)
	}

	// Start API server
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/ws", apiServer.HandleWebSocket)
		mux.HandleFunc("/api/requests", apiServer.HandleGetRequests)
		log.Printf("API/WebSocket server listening on :%s", *apiPort)
		if err := http.ListenAndServe(":"+*apiPort, mux); err != nil {
			log.Fatalf("API server error: %v", err)
		}
	}()

	// Start Proxy server
	log.Printf("Proxy server listening on :%s", *proxyPort)
	if err := http.ListenAndServe(":"+*proxyPort, proxyServer); err != nil {
		log.Fatalf("Proxy server error: %v", err)
	}
}
