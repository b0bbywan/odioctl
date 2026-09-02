package main

import (
	"os"

	"github.com/b0bbywan/odioctl/cli"
)

func main() {
	os.Exit(cli.Run(os.Stdout, os.Stderr, os.Args[1:]))
}
