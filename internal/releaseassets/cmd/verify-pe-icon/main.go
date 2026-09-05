package main

import (
	"fmt"
	"os"

	"github.com/openvibely/openvibely/internal/releaseassets"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: verify-pe-icon <windows-executable>")
		os.Exit(2)
	}
	if err := releaseassets.VerifyPEIcon(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
