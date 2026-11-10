package main

import (
	"atlas/internal/eventauditcli"
	"os"
)

func main() { os.Exit(eventauditcli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
