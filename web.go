package web

import "embed"

// Assets contains only public application resources, never repository files.
//
//go:embed index.html LICENSE assets
var Assets embed.FS
