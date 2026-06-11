// Package assets embeds static web files into the binary.
package assets

import "embed"

// FS holds the embedded web directory.
//
//go:embed web/*
var FS embed.FS
