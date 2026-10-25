package main

import (
	"embed"

	"github.com/dlcuy22/molow/backend"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	backend.Run(assets)
}
