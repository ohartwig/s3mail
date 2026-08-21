// Kleines Programm, das die MIME-Schicht benutzt - nur um zu sehen, was ein
// fertiges Binary pro Plattform wiegt und ob die Cross-Kompilierung durchlaeuft.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"s3mail/mimeparse"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: probe <datei.eml>")
		return
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s := mimeparse.Summarize(raw, time.Unix(0, 0).UTC())
	out, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(out))
}
