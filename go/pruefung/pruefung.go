// Package pruefung ist der Verbindungstest aus Schritt 3 des Assistenten: er geht
// der Reihe nach durch, was s3mail braucht, und schreibt zu jedem fehlenden Punkt
// die IAM-Aktion dazu, die dafuer noetig waere. Das ist die Stelle, an der jemand
// ohne AWS-Vorwissen erfaehrt, woran es liegt.
package pruefung

import (
	"context"
	"fmt"
	"strings"

	"s3mail/store"
)

// Punkt ist ein Eintrag der Checkliste.
type Punkt struct {
	Name        string `json:"name"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail"`
	Hinweis     string `json:"hint"`
	Uebergangen bool   `json:"skipped"`
}

// Umgebung ist, was der Test braucht. Als Schnittstelle, damit er ohne AWS laeuft.
type Umgebung interface {
	store.S3
	Buckets(ctx context.Context) ([]string, error)
	LifecycleTage(ctx context.Context, bucket string) int
}

// SESPruefer meldet, ob eine Absenderadresse in SES freigeschaltet ist.
type SESPruefer interface {
	Verifiziert(ctx context.Context, adresse, domain string) ([]string, error)
}

// Ausfuehren geht die Liste durch. probeSchreiben legt ein Testobjekt an und
// loescht es wieder - nur so laesst sich das Schreibrecht ehrlich pruefen.
func Ausfuehren(ctx context.Context, s3 store.S3, kms store.KMS, ses SESPruefer,
	bucket, prefix, absender string) []Punkt {
	var punkte []Punkt
	add := func(p Punkt) { punkte = append(punkte, p) }

	// 1. Auflisten
	objs, err := s3.List(ctx, bucket, prefix)
	if err != nil {
		add(Punkt{Name: "Bucket lesen", Detail: kurz(err),
			Hinweis: hinweisAuflisten(prefix)})
		return punkte
	}
	var beispiel string
	for _, o := range objs {
		if o.Size > 0 && !istIntern(o.Key, prefix) {
			beispiel = o.Key
			break
		}
	}
	if beispiel != "" {
		add(Punkt{Name: "Bucket lesen", OK: true,
			Detail: fmt.Sprintf("%d Objekt(e) unter „%s“ gefunden", len(objs), anzeige(prefix))})
	} else {
		add(Punkt{Name: "Bucket lesen", OK: true,
			Detail:  fmt.Sprintf("Zugriff klappt, aber unter „%s“ liegt noch nichts", anzeige(prefix)),
			Hinweis: "Sobald SES die erste Mail ablegt, taucht sie hier auf."})
	}

	// 2. Eine echte Mail lesen - und dabei sehen, wie sie verschluesselt ist
	if beispiel == "" {
		add(Punkt{Name: "Mail lesen", OK: true, Detail: "übersprungen – noch keine Mail da",
			Uebergangen: true})
		add(Punkt{Name: "Verschlüsselung", OK: true, Detail: "übersprungen – noch keine Mail da",
			Uebergangen: true})
	} else {
		obj, err := s3.Get(ctx, bucket, beispiel, "bytes=0-2047")
		if err != nil {
			add(Punkt{Name: "Mail lesen", Detail: kurz(err), Hinweis: "s3:GetObject fehlt."})
		} else {
			add(Punkt{Name: "Mail lesen", OK: true, Detail: basisname(beispiel)})
			add(verschluesselung(ctx, s3, kms, bucket, beispiel, obj))
		}
	}

	// 3./4. Schreiben und Loeschen an einem Testobjekt
	probe := prefix + ".s3mail-probe"
	if err := s3.Put(ctx, bucket, probe, []byte("s3mail"), "text/plain"); err != nil {
		add(Punkt{Name: "Schreiben", Detail: kurz(err),
			Hinweis: "s3:PutObject fehlt. Ohne das gehen Tags, Verschieben und Papierkorb nicht."})
		add(Punkt{Name: "Löschen", Detail: "übersprungen", Uebergangen: true, OK: true})
	} else {
		add(Punkt{Name: "Schreiben", OK: true, Detail: "Testobjekt angelegt"})
		if err := s3.Delete(ctx, bucket, probe); err != nil {
			add(Punkt{Name: "Löschen", Detail: kurz(err),
				Hinweis: "s3:DeleteObject fehlt. Starte mit „Löschen sperren“, dann bleibt alles beim Lesen."})
		} else {
			add(Punkt{Name: "Löschen", OK: true, Detail: "Testobjekt wieder entfernt"})
		}
	}

	// 5. Der Ordner, in dem die Zustandsaenderungen liegen
	if _, err := s3.List(ctx, bucket, prefix+store.StateOps); err != nil {
		add(Punkt{Name: "Zustand von mehreren Rechnern", Detail: kurz(err),
			Hinweis: fmt.Sprintf("s3:ListBucket auf %s%s* fehlt. Ohne das sieht dieser "+
				"Rechner Änderungen der anderen erst nach dem nächsten Zusammenfassen.",
				prefix, store.StateOps)})
	} else {
		add(Punkt{Name: "Zustand von mehreren Rechnern", OK: true,
			Detail: "Ordner für die Änderungen ist lesbar"})
	}

	// 6. SES-Absender
	if absender == "" {
		add(Punkt{Name: "SES-Absender", OK: true, Uebergangen: true,
			Detail: "keine Adresse angegeben – Antworten bleibt aus"})
	} else if ses == nil {
		add(Punkt{Name: "SES-Absender", OK: true, Uebergangen: true, Detail: "nicht geprüft"})
	} else {
		domain := absender
		if i := strings.LastIndex(absender, "@"); i >= 0 {
			domain = absender[i+1:]
		}
		gut, err := ses.Verifiziert(ctx, absender, domain)
		switch {
		case err != nil:
			add(Punkt{Name: "SES-Absender", Detail: kurz(err), Uebergangen: true,
				Hinweis: "ses:GetIdentityVerificationAttributes fehlt – prüfe die Adresse selbst."})
		case len(gut) > 0:
			add(Punkt{Name: "SES-Absender", OK: true, Detail: "verifiziert: " + strings.Join(gut, ", ")})
		default:
			add(Punkt{Name: "SES-Absender", Detail: absender + " ist in SES nicht verifiziert",
				Hinweis: "Adresse oder Domain in der SES-Konsole verifizieren – sonst lehnt SES den Versand ab."})
		}
	}
	return punkte
}

// verschluesselung schaut sich eine echte Mail an und sagt, womit man es zu tun hat.
func verschluesselung(ctx context.Context, s3 store.S3, kms store.KMS,
	bucket, key string, obj store.Object) Punkt {
	if store.IstUmschlag(obj.Meta) {
		// Ein Teilstueck laesst sich nicht entschluesseln - also ganz holen.
		voll, err := s3.Get(ctx, bucket, key, "")
		if err == nil {
			_, err = store.Entschluesseln(voll.Body, voll.Meta, kms)
		}
		if err != nil {
			return Punkt{Name: "Verschlüsselung",
				Detail:  "client-seitig mit KMS – Entschlüsseln klappt nicht: " + kurz(err),
				Hinweis: "kms:Decrypt auf dem Schlüssel aus der SES-Regel fehlt."}
		}
		return Punkt{Name: "Verschlüsselung", OK: true,
			Detail: "client-seitig mit KMS – Entschlüsseln klappt"}
	}
	head, err := s3.Head(ctx, bucket, key)
	if err != nil {
		return Punkt{Name: "Verschlüsselung", OK: true, Uebergangen: true, Detail: kurz(err)}
	}
	switch {
	case strings.Contains(head.ServerSideEncryption, "kms"):
		return Punkt{Name: "Verschlüsselung", OK: true, Detail: "serverseitig mit KMS",
			Hinweis: "Die IAM-Rolle braucht kms:Decrypt und kms:GenerateDataKey."}
	case head.ServerSideEncryption != "":
		return Punkt{Name: "Verschlüsselung", OK: true,
			Detail: "serverseitig (" + head.ServerSideEncryption + ")"}
	default:
		return Punkt{Name: "Verschlüsselung", OK: true, Detail: "keine – die Mails liegen im Klartext"}
	}
}

// Alles sagt, ob die Liste insgesamt in Ordnung ist.
func Alles(punkte []Punkt) bool {
	for _, p := range punkte {
		if !p.OK && !p.Uebergangen {
			return false
		}
	}
	return true
}

func istIntern(key, prefix string) bool {
	for _, teil := range strings.Split(strings.TrimPrefix(key, prefix), "/") {
		if strings.HasPrefix(teil, ".") {
			return true
		}
	}
	return false
}

func anzeige(prefix string) string {
	if prefix == "" {
		return "/"
	}
	return prefix
}

func basisname(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func kurz(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// hinweisAuflisten nennt die drei Ursachen in der Reihenfolge ihrer
// Wahrscheinlichkeit - und die haeufigste zuerst.
//
// Ist der Zugang auf ein eigenes Prefix beschraenkt (ein Postfach pro Person),
// dann scheitert das Auflisten an einem Prefix, das auch nur eine Ebene zu weit
// oben liegt: die Bedingung s3:prefix vergleicht die Zeichenkette, nicht den
// Pfad. "mail/" passt nicht auf "mail/person/*", und "mail/person" ohne
// abschliessenden Schraegstrich ebenfalls nicht. Das sieht wie ein fehlendes
// Recht aus und ist eine fehlende Stelle.
func hinweisAuflisten(prefix string) string {
	if prefix == "" {
		return "Fehlt s3:ListBucket, oder Bucket und Region passen nicht zueinander."
	}
	return fmt.Sprintf(
		"Am wahrscheinlichsten stimmt das Prefix nicht: Zugänge, die nur ein "+
			"eigenes Postfach sehen dürfen, brauchen es exakt – „%s“ mit "+
			"abschließendem Schrägstrich, nicht die Ebene darüber. Sonst fehlt "+
			"s3:ListBucket, oder Bucket und Region passen nicht zueinander.",
		prefix)
}
