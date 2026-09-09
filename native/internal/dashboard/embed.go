// Package dashboard embeds the static SPA files so the single binary can
// serve them without a filesystem dependency.
package dashboard

import "embed"

//go:embed static/*
var FS embed.FS
