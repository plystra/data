package main

import (
	"os"

	"github.com/plystra/data/protocol"
)

func main() {
	if err := protocol.Run(os.Stdin, os.Stdout); err != nil {
		_, _ = os.Stderr.WriteString("plystra data compiler protocol failed\n")
		os.Exit(1)
	}
}
