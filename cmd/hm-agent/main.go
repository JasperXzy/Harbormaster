package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/JasperXzy/harbormaster/internal/agentcli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(agentcli.Agent(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
