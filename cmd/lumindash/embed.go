package main

import "embed"

//go:embed all:../../internal/templates
var staticFiles embed.FS
