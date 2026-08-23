// Package web ist der lokale Server: Routen, Zugangskontrolle und die
// Oberflaeche. Die Seiten liegen als echte Dateien daneben und werden
// einkompiliert - kein Generator, kein Zusammenkleben von Zeichenketten.
package web

import _ "embed"

//go:embed assets/inbox.html
var PageMailbox string

//go:embed assets/setup.html
var PageWizard string

//go:embed assets/token.html
var PageToken string
