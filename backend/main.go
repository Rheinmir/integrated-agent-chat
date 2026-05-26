package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Rheinmir/integrated-agent-chat/internal/api"
	"github.com/Rheinmir/integrated-agent-chat/internal/db"
	"github.com/Rheinmir/integrated-agent-chat/internal/mcp"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/agent.db"
	}

	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	tools := mcp.NewRegistry(database)

	handlers := &api.AIHandlers{
		AnthropicKey:  os.Getenv("ANTHROPIC_API_KEY"),
		GeminiKey:     os.Getenv("GEMINI_API_KEY"),
		OpenRouterKey: os.Getenv("OPENROUTER_API_KEY"),
		Tools:         tools,
		DB:            database,
	}

	router := api.NewRouter(handlers)

	log.Printf("Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
