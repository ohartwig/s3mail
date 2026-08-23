// Package pruefung ist der Verbindungstest aus Schritt 3 des Assistenten: er geht
// der Reihe nach durch, was s3mail braucht, und schreibt zu jedem fehlenden Punkt
// die IAM-Aktion dazu, die dafuer noetig waere. Das ist die Stelle, an der jemand
// ohne AWS-Vorwissen erfaehrt, woran es liegt.
package check

import (
	"context"
	"s3mail/i18n"
	"strings"

	"s3mail/store"
)

// Item ist ein Eintrag der Checkliste.
type Item struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	Hint    string `json:"hint"`
	Skipped bool   `json:"skipped"`
}

// Environment ist, was der Test braucht. Als Schnittstelle, damit er ohne AWS laeuft.
type Environment interface {
	store.S3
	Buckets(ctx context.Context) ([]string, error)
	LifecycleTage(ctx context.Context, bucket string) int
}

// SESChecker meldet, ob eine Absenderadresse in SES freigeschaltet ist.
type SESChecker interface {
	Verifiziert(ctx context.Context, adresse, domain string) ([]string, error)
}

// Run geht die Liste durch. probeSchreiben legt ein Testobjekt an und
// loescht es wieder - nur so laesst sich das Schreibrecht ehrlich pruefen.
func Run(ctx context.Context, s3 store.S3, kms store.KMS, ses SESChecker,
	bucket, prefix, absender string, cat i18n.Catalog) []Item {
	var punkte []Item
	add := func(p Item) { punkte = append(punkte, p) }

	// 1. Auflisten
	objs, err := s3.List(ctx, bucket, prefix)
	if err != nil {
		add(Item{Name: cat.T("check.listBucket"), Detail: short(err),
			Hint: listHint(prefix, cat)})
		return punkte
	}
	var beispiel string
	for _, o := range objs {
		if o.Size > 0 && !isInternal(o.Key, prefix) {
			beispiel = o.Key
			break
		}
	}
	if beispiel != "" {
		add(Item{Name: cat.T("check.listBucket"), OK: true,
			Detail: cat.Tf("check.listBucket.found", len(objs), display(prefix))})
	} else {
		add(Item{Name: cat.T("check.listBucket"), OK: true,
			Detail: cat.Tf("check.listBucket.empty", display(prefix)),
			Hint:   cat.T("check.listBucket.emptyHint")})
	}

	// 2. Eine echte Mail lesen - und dabei sehen, wie sie verschluesselt ist
	if beispiel == "" {
		add(Item{Name: cat.T("check.readMail"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Skipped: true})
		add(Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.skipped.noMail"),
			Skipped: true})
	} else {
		obj, err := s3.Get(ctx, bucket, beispiel, "bytes=0-2047")
		if err != nil {
			add(Item{Name: cat.T("check.readMail"), Detail: short(err), Hint: cat.T("check.readMail.hint")})
		} else {
			add(Item{Name: cat.T("check.readMail"), OK: true, Detail: baseName(beispiel)})
			add(encryption(ctx, s3, kms, bucket, beispiel, obj, cat))
		}
	}

	// 3./4. Schreiben und Loeschen an einem Testobjekt
	probe := prefix + ".s3mail-probe"
	if err := s3.Put(ctx, bucket, probe, []byte("s3mail"), "text/plain"); err != nil {
		add(Item{Name: cat.T("check.write"), Detail: short(err),
			Hint: cat.T("check.write.hint")})
		add(Item{Name: cat.T("check.delete"), Detail: cat.T("check.skipped"), Skipped: true, OK: true})
	} else {
		add(Item{Name: cat.T("check.write"), OK: true, Detail: cat.T("check.write.ok")})
		if err := s3.Delete(ctx, bucket, probe); err != nil {
			add(Item{Name: cat.T("check.delete"), Detail: short(err),
				Hint: cat.T("check.delete.hint")})
		} else {
			add(Item{Name: cat.T("check.delete"), OK: true, Detail: cat.T("check.delete.ok")})
		}
	}

	// 5. Der Ordner, in dem die Zustandsaenderungen liegen
	if _, err := s3.List(ctx, bucket, prefix+store.StateOps); err != nil {
		add(Item{Name: cat.T("check.sharedState"), Detail: short(err),
			Hint: cat.Tf("check.sharedState.hint", prefix, store.StateOps)})
	} else {
		add(Item{Name: cat.T("check.sharedState"), OK: true,
			Detail: cat.T("check.sharedState.ok")})
	}

	// 6. SES-Absender
	if absender == "" {
		add(Item{Name: cat.T("check.sender"), OK: true, Skipped: true,
			Detail: cat.T("check.sender.none")})
	} else if ses == nil {
		add(Item{Name: cat.T("check.sender"), OK: true, Skipped: true, Detail: cat.T("check.notChecked")})
	} else {
		domain := absender
		if i := strings.LastIndex(absender, "@"); i >= 0 {
			domain = absender[i+1:]
		}
		gut, err := ses.Verifiziert(ctx, absender, domain)
		switch {
		case err != nil:
			add(Item{Name: cat.T("check.sender"), Detail: short(err), Skipped: true,
				Hint: cat.T("check.sender.noPermission")})
		case len(gut) > 0:
			add(Item{Name: cat.T("check.sender"), OK: true, Detail: cat.Tf("check.sender.verified", strings.Join(gut, ", "))})
		default:
			add(Item{Name: cat.T("check.sender"), Detail: cat.Tf("check.sender.unverified", absender),
				Hint: cat.T("check.sender.hint")})
		}
	}
	return punkte
}

// encryption schaut sich eine echte Mail an und sagt, womit man es zu tun hat.
func encryption(ctx context.Context, s3 store.S3, kms store.KMS,
	bucket, key string, obj store.Object, cat i18n.Catalog) Item {
	if store.IsEnvelope(obj.Meta) {
		// Ein Teilstueck laesst sich nicht entschluesseln - also ganz holen.
		voll, err := s3.Get(ctx, bucket, key, "")
		if err == nil {
			_, err = store.Decrypt(voll.Body, voll.Meta, kms)
		}
		if err != nil {
			return Item{Name: cat.T("check.encryption"),
				Detail: cat.Tf("check.encryption.clientFailed", short(err)),
				Hint:   cat.T("check.encryption.clientHint")}
		}
		return Item{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.T("check.encryption.clientOk")}
	}
	head, err := s3.Head(ctx, bucket, key)
	if err != nil {
		return Item{Name: cat.T("check.encryption"), OK: true, Skipped: true, Detail: short(err)}
	}
	switch {
	case strings.Contains(head.ServerSideEncryption, "kms"):
		return Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.serverKms"),
			Hint: cat.T("check.encryption.serverKmsHint")}
	case head.ServerSideEncryption != "":
		return Item{Name: cat.T("check.encryption"), OK: true,
			Detail: cat.Tf("check.encryption.server", head.ServerSideEncryption)}
	default:
		return Item{Name: cat.T("check.encryption"), OK: true, Detail: cat.T("check.encryption.none")}
	}
}

// AllOK sagt, ob die Liste insgesamt in Ordnung ist.
func AllOK(punkte []Item) bool {
	for _, p := range punkte {
		if !p.OK && !p.Skipped {
			return false
		}
	}
	return true
}

func isInternal(key, prefix string) bool {
	for _, teil := range strings.Split(strings.TrimPrefix(key, prefix), "/") {
		if strings.HasPrefix(teil, ".") {
			return true
		}
	}
	return false
}

func display(prefix string) string {
	if prefix == "" {
		return "/"
	}
	return prefix
}

func baseName(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

func short(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// listHint nennt die drei Ursachen in der Reihenfolge ihrer
// Wahrscheinlichkeit - und die haeufigste zuerst.
//
// Ist der Zugang auf ein eigenes Prefix beschraenkt (ein Postfach pro Person),
// dann scheitert das Auflisten an einem Prefix, das auch nur eine Ebene zu weit
// oben liegt: die Bedingung s3:prefix vergleicht die Zeichenkette, nicht den
// Pfad. "mail/" passt nicht auf "mail/person/*", und "mail/person" ohne
// abschliessenden Schraegstrich ebenfalls nicht. Das sieht wie ein fehlendes
// Recht aus und ist eine fehlende Stelle.
func listHint(prefix string, cat i18n.Catalog) string {
	if prefix == "" {
		return cat.T("check.listBucket.hintNoPrefix")
	}
	return cat.Tf("check.listBucket.hintPrefix", prefix)
}
