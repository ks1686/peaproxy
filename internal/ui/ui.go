package ui

import "embed"

// FS is the localhost dashboard (Accounts, Catalog, Showcase, Clients, Health, Settings).
//
//go:embed web
var FS embed.FS
