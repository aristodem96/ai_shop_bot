// Package data embeds the reference files the catalogue is built on, so a
// binary carries them and every install starts from the same ones.
package data

import _ "embed"

// Categories is the category tree with the keywords that sort imported rows
// into it. It is synced into the database on start.
//
//go:embed categories.csv
var Categories []byte
