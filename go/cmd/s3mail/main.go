// s3mail - Mail-Client fuer E-Mails, die Amazon SES in einen S3-Bucket legt.
//
// Dieser Einstiegspunkt ist noch unvollstaendig: Konfiguration, Assistent und
// SES-Versand fehlen. Er zeigt, dass Oberflaeche und Server als eine Datei ohne
// Entpacken laufen.
package main

import (
	"fmt"

	"s3mail/web"
)

func main() {
	fmt.Println("s3mail (Portierung im Bau)")
	fmt.Printf("  Oberflaeche eingebettet: %d Zeichen Postfach, %d Assistent\n",
		len(web.SeitePostfach), len(web.SeiteAssistent))
	fmt.Printf("  Beispiel-Token: %s…\n", web.NeuesToken()[:12])
}
