//go:build 386

package miniblink

import (
	"embed"
)

const (
	ARCH    = "x32"
	VERSION = "4957"
)

//go:embed release/x32
var res embed.FS
