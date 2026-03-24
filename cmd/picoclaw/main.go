// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package main

import (
	"embed"
	"fmt"
	"os"
)

//go:generate cp -r ../../workspace .
//go:generate cp -r ../../pitch .
//go:embed workspace
var embeddedFiles embed.FS

//go:embed pitch
var pitchFiles embed.FS

var (
	version   = "dev"
	gitCommit string
	buildTime string
	goVersion string
)

const logo = "🦞"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "onboard":
		onboard()
	case "agent":
		agentCmd()
	case "gateway":
		gatewayCmd()
	case "status":
		statusCmd()
	case "migrate":
		migrateCmd()
	case "auth":
		authCmd()
	case "cron":
		cronCmd()
	case "skills":
		skillsCmd()
	case "version", "--version", "-v":
		printVersion()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printHelp()
		os.Exit(1)
	}
}
