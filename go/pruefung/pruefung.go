// Package pruefung ist der Verbindungstest aus Schritt 3 des Assistenten: er geht
// der Reihe nach durch, was s3mail braucht, und schreibt zu jedem fehlenden Punkt
// die IAM-Aktion dazu, die dafuer noetig waere. Das ist die Stelle, an der jemand
// ohne AWS-Vorwissen erfaehrt, woran es liegt.
package pruefung

import (
	"context"
	"s3mail/i18n"
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
	bucket, prefix, absender string, cat i18n.Catalog) []Punkt {
	var punkte []Punkt
	add := func(p Punkt) { punkte = append(punkte, p) }

	// 1. Auflisten
	objs, err := s3.List(ctx, bucket, prefix)
	if err != nil {
		add(Punkt{Name: cat.T("check.listBucket"), Detail: kurz(err),
			Hinweis: hinweisAuflisten(prefix, cat)})
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
		add(Punkt{Name: cat.T("check.listBucket"), OK: true,
			Detail: cat.Tf("check.listBucket.found", len(objs), anzeige(prefix))})
	} else {
		add(Punkt{Name: cat.T("check.listBucket"), OK: true,
			Detail:  cat.Tf("check.listBucket.empty", anzeige(prefix)),
			Hinweis: cat.T("check.listBucket.emptyHint")})
	}

	// 2. Eine echte Mail lesen - und dabei sehen, wie sie verschluesselt ist
	if beispiel == "" {
		add(Punkt{Name: cat.T("check.readMail"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Uebergangen: true})
		add(Punkt{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Uebergangen: true})
	} else {
		obj, err := s3.Get(ctx, bucket, beispiel, "bytes=0-2047")
		if err != nil {
			add(Punkt{Name: cat.T("check.readMail"), Detail: kurz(err), Hinweis: cat.T("check.readMail.hint")})
		} else {
			add(Punkt{Name: cat.T("check.readMail"), OK: true, Detail: basisname(beispiel)})
			add(verschluesselung(ctx, s3, kms, bucket, beispiel, obj, cat))
		}
	}

	// 3./4. Schreiben und Loeschen an einem Testobjekt
	probe := prefix + ".s3mail-probe"
	if err := s3.Put(ctx, bucket, probe, []byte("s3mail"), "text/plain"); err != nil {
		add(Punkt{Name: cat.T("check.write"), Detail: kurz(err),
			Hinweis: cat.T("check.write.hint")})
		add(Punkt{Name: cat.T("check.delete"), Detail: cat.T("check.skipped"), Uebergangen: true, OK: true})
	} else {
		add(Punkt{Name: cat.T("check.write"), OK: true, Detail: cat.T("check.write.ok")})
		if err := s3.Delete(ctx, bucket, probe); err != nil {
			add(Punkt{Name: cat.T("check.delete"), Detail: kurz(err),
				Hinweis: cat.T("check.delete.hint")})
		} else {
			add(Punkt{Name: cat.T("check.delete"), OK: true, Detail: cat.T("check.delete.ok")})
		}
	}

	// 5. Der Ordner, in dem die Zustandsaenderungen liegen
	if _, err := s3.List(ctx, bucket, prefix+store.StateOps); err != nil {
		add(Punkt{Name: cat.T("check.sharedState"), Detail: kurz(err),
			Hinweis: cat.Tf("check.sharedState.hint", prefix, store.StateOps)})
	} else {
		add(Punkt{Name: cat.T("check.sharedState"), OK: true,
			Detail: cat.T("check.sharedState.ok")})
	}

	// 6. SES-Absender
	if absender == "" {
		add(Punkt{Name: cat.T("check.sender"), OK: true, Uebergangen: true,
			Detail: cat.T("check.sender.none")})
	} else if ses == nil {
		add(Punkt{Name: cat.T("check.sender"), OK: true, Uebergangen: true, Detail: cat.T("check.notChecked")})
	} else {
		domain := absender
		if i := strings.LastIndex(absender, "@"); i >= 0 {
			domain = absender[i+1:]
		}
		gut, err := ses.Verifiziert(ctx, absender, domain)
		switch {
		case err != nil:
			add(Punkt{Name: cat.T("check.sender"), Detail: kurz(err), Uebergangen: true,
				Hinweis: cat.T("check.sender.noPermission")})
		case len(gut) > 0:
			add(Punkt{Name: cat.T("check.sender"), OK: true, Detail: cat.Tf("check.sender.verified", strings.Join(gut, ", "))})
		default:
			add(Punkt{Name: cat.T("check.sender"), Detail: cat.Tf("check.sender.unverified", absender),
				Hinweis: cat.T("check.sender.hint")})
		}
	}
	return punkte
}

// verschluesselung schaut sich eine echte Mail an und sagt, womit man es zu tun hat.
func verschluesselung(ctx context.Context, s3 store.S3, kms store.KMS,
	bucket, key string, obj store.Object, cat i18n.Catalog) Punkt {
	if store.IstUmschlag(obj.Meta) {
		// Ein Teilstueck laesst sich nicht entschluesseln - also ganz holen.
		voll, err := s3.Get(ctx, bucket, key, "")
		if err == nil {
			_, err = store.Entschluesseln(voll.Body, voll.Meta, kms)
		}
		if err != nil {
			return Punkt{Name: cat.T("check.encryption"),
				Detail:  cat.Tf("check.encryption.clientFailed", kurz(err)),
				Hinweis: cat.T("check.encryption.clientHint")}
		}
		return Punkt{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.T("check.encryption.clientOk")}
	}
	head, err := s3.Head(ctx, bucket, key)
	if err != nil {
		return Punkt{Name: cat.T("check.encryption"), OK: true, Uebergangen: true, Detail: kurz(err)}
	}
	switch {
	case strings.Contains(head.ServerSideEncryption, "kms"):
		return Punkt{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.serverKms"),
			Hinweis: cat.T("check.encryption.serverKmsHint")}
	case head.ServerSideEncryption != "":
		return Punkt{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.Tf("check.encryption.server", head.ServerSideEncryption)}
	default:
		return Punkt{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.none")}
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
func hinweisAuflisten(prefix string, cat i18n.Catalog) string {
	if prefix == "" {
		return cat.T("check.listBucket.hintNoPrefix")
	}
	return cat.Tf("check.listBucket.hintPrefix", prefix)
}
