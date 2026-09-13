package main

import (
	"context"
	"fmt"
	"os"
	"time"

	workflowpostgres "github.com/36Dge/yijie-api/internal/modules/workflows/infrastructure/postgres"
	"github.com/36Dge/yijie-api/internal/workflowlocal"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] != "up" {
		fmt.Fprintln(os.Stderr, "usage: workflow-local-migrate up")
		os.Exit(2)
	}
	config, err := workflowlocal.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "workflow migration configuration invalid")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if workflowpostgres.Migrate(ctx, config.DSN) != nil {
		fmt.Fprintln(os.Stderr, "workflow migration unavailable")
		os.Exit(1)
	}
	fmt.Println("workflow schema ready")
}
