// SPDX-License-Identifier: GPL-3.0-or-later

// Package proto holds the plugin protocol definitions. The .proto files
// live in tessera/v1, next to the Go code generated from them (package
// tesserav1). See docs/protocol.md.
package proto

//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.74.0 generate
