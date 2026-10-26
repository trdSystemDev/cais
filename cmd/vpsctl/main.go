package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/trdSystemDev/vpsctl/internal/core"
	"github.com/trdSystemDev/vpsctl/internal/ui"
)

// version e preenchido no build via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "mostra a versao e sai")
	flag.Parse()

	if *showVersion {
		fmt.Println("vpsctl", version)
		return
	}

	c := core.New(core.Deps{})
	if err := ui.New(c).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "vpsctl:", err)
		os.Exit(1)
	}
}
