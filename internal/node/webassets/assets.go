// Package webassets exposes the production Node control center bundled into
// the Yuanshu binary. The generated distribution is committed so Go builds do
// not require a Node.js toolchain.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

func FS() (fs.FS, error) { return fs.Sub(embedded, "dist") }
