// Command player2 is a terminal music player.
//
// The CLI is intentionally minimal for now; playback and the TUI are wired up
// in later milestones.
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("player2 %s\n", version)
		return
	}

	fmt.Fprintln(os.Stderr, "player2: interactive mode is not implemented yet (try --version)")
	os.Exit(2)
}
