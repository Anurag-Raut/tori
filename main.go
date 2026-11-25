package main

import (
	"os"

	"github.com/Anurag-Raut/tori/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(os.Args[1:], version))
}
