package main

import (
	"context"
	"fmt"
	"os"

	"github.com/36Dge/yijie-api/internal/contracts/pin"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/contract-pin <source|generated|generator> <lock-file> <repository-root>")
		os.Exit(2)
	}
	lock, err := pin.Load(os.Args[2])
	if err == nil {
		switch os.Args[1] {
		case "source":
			err = pin.VerifySource(context.Background(), lock, os.Args[3])
		case "generated":
			err = pin.VerifyGenerated(lock, os.Args[3])
		case "generator":
			err = pin.VerifyGenerator(context.Background(), lock, os.Args[3])
		default:
			err = fmt.Errorf("unsupported contract pin command %q", os.Args[1])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
