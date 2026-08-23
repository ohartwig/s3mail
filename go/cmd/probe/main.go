// A small program that uses the MIME layer - only to see what a finished
// binary weighs per platform and whether cross compilation goes through.
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
