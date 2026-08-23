// Package web is the local server: routes, access control and the interface. The
// pages lie next to it as real files and are compiled in - no generator, no
// gluing of strings.
package web

import _ "embed"

//go:embed assets/inbox.html
var PageMailbox string

//go:embed assets/setup.html
var PageWizard string

//go:embed assets/token.html
var PageToken string
