// Command march is a terminal interface for installing, starting and managing
// Arch Linux ARM virtual machines on QEMU.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/ui"
)

// version is overridable at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print the version and exit")
		root        = flag.String("home", "", "directory for VMs and images (default: $XDG_DATA_HOME/march)")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("march", version)
		return
	}

	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "march:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	store, err := config.NewStore(root)
	if err != nil {
		return err
	}

	model := ui.New(store)
	p := tea.NewProgram(model)
	// The model pushes download progress from a background goroutine, which
	// needs a handle on the running program.
	model.SetProgram(p)

	_, err = p.Run()
	return err
}

func usage() {
	fmt.Fprint(os.Stderr, `march — install and manage Arch Linux ARM VMs on QEMU

Usage:
  march [flags]

Flags:
  -home dir    store VMs and installer images under dir
  -version     print the version and exit

march needs QEMU with aarch64 support:
  macOS   brew install qemu
  Debian  apt install qemu-system-arm qemu-utils
  Arch    pacman -S qemu-system-aarch64 edk2-aarch64
`)
}
