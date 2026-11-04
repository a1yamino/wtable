package main

import (
	"fmt"
	"os"

	"wtable/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wtable:", err)
		os.Exit(1)
	}
}
