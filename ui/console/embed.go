// Package consoleui embeds the built fleet console SPA so the bp daemon can
// serve it with no external files (docs/fleet-console.md, WS5). The bundle under
// dist/ is produced off this machine by "npm ci && npm run build" in this
// directory; a minimal dist/index.html placeholder is committed so this package
// compiles before that build has ever run.
//
// The //go:embed directive has to live beside the embedded directory (Go forbids
// a "../" embed path), so the embed source sits here in ui/console and the HTTP
// handler that serves it lives in internal/dashboard/console.go, which imports
// this package.
package consoleui

import "embed"

// Assets holds the built SPA: dist/index.html and the hashed asset files Vite
// emits beside it. The "all:" prefix keeps files the default embed would skip
// (dotfiles such as the committed .gitkeep placeholder), so nothing the build
// produced is silently dropped from the served bundle.
//
//go:embed all:dist
var Assets embed.FS
