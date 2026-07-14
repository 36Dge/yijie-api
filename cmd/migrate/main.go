package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/36Dge/yijie-api/internal/platform/migrations"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/migrate <up|down|status>")
		os.Exit(2)
	}
	dsn := os.Getenv("YIJIE_API_POSTGRES_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "YIJIE_API_POSTGRES_DSN is required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.Run(ctx, dsn, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
