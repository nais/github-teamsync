package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/nais/nais-github-teamsync/internal/cmd/reconciler"
)

func main() {
	if err := reconciler.Run(context.Background()); err != nil {
		slog.Error("nais-github-teamsync stopped", "error", err)
		os.Exit(1)
	}
}
