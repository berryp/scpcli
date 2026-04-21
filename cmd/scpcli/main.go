package main

import (
	"fmt"
	"os"

	"github.com/berryp/scpcli/internal/commands"
)

func main() {
	if err := commands.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
