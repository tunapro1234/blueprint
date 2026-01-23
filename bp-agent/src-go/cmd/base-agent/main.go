package main

import (
	"log"
	"os"
	"strconv"

	agent "github.com/tunapro1234/base-agent/src-go"
	"github.com/tunapro1234/base-agent/src-go/api"
)

func main() {
	port := 8080
	if raw := os.Getenv("PORT"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}

	inst := agent.New("go-agent", agent.DefaultAgentConfig(), "")
	server := api.NewAgentServer(port, inst)
	log.Printf("base-agent listening on :%d", port)
	if err := server.Start(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
