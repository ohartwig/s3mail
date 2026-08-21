package awsx

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Dieses Stueck ist absichtlich frei von AWS-Aufrufen: was in einem
// Policy-Dokument steht, laesst sich ohne Konto pruefen, und genau daran haengt,
// ob der Assistent das Richtige vorschlaegt.

// ausPolicies liest Bucket, Prefix und Absender aus IAM-Policy-Dokumenten.
func ausPolicies(dokumente []string) Fund {
	var f Fund
	for _, d := range dokumente {
		for _, s := range statements(d) {
			if !strings.EqualFold(s.Effect, "Allow") {
				continue // ein Deny sagt nichts darueber, wo das Postfach liegt
			}
			aktionen := liste(s.Action)
			switch {
			case hatPraefix(aktionen, "s3:"):
				bucket, prefix := ausS3(aktionen, liste(s.Resource), s.Condition)
				// Der laengste Prefix gewinnt: eine Policy darf den Bucket
				// grob und das eigene Postfach genau nennen.
				if bucket != "" && (f.Bucket == "" || len(prefix) > len(f.Prefix)) {
					f.Bucket, f.Prefix = bucket, prefix
				}
			case hatPraefix(aktionen, "ses:"):
				if a := absenderAus(s.Condition); a != "" {
					f.Absender = a
				}
			}
		}
	}
	return f
}

type statement struct {
	Effect    string                                `json:"Effect"`
	Action    json.RawMessage                       `json:"Action"`
	Resource  json.RawMessage                       `json:"Resource"`
	Condition map[string]map[string]json.RawMessage `json:"Condition"`
}

// statements packt ein Policy-Dokument aus. IAM liefert es url-kodiert, und
// "Statement" darf ein einzelnes Objekt oder eine Liste sein.
func statements(dokument string) []statement {
	if s, err := url.QueryUnescape(dokument); err == nil {
		dokument = s
	}
	var d struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if json.Unmarshal([]byte(dokument), &d) != nil {
		return nil
	}
	var viele []statement
	if json.Unmarshal(d.Statement, &viele) == nil {
		return viele
	}
	var eins statement
	if json.Unmarshal(d.Statement, &eins) == nil {
		return []statement{eins}
	}
	return nil
}

// liste nimmt "s3:GetObject" genauso wie ["s3:GetObject", "s3:PutObject"].
func liste(r json.RawMessage) []string {
	if len(r) == 0 {
		return nil
	}
	var viele []string
	if json.Unmarshal(r, &viele) == nil {
		return viele
	}
	var eins string
	if json.Unmarshal(r, &eins) == nil {
		return []string{eins}
	}
	return nil
}

func hatPraefix(werte []string, p string) bool {
	for _, w := range werte {
		if strings.HasPrefix(strings.ToLower(w), p) {
			return true
		}
	}
	return false
}

// ausS3 zieht Bucket und Prefix aus den Resource-ARNs einer S3-Erlaubnis.
// Fehlt der Prefix dort, hilft die Bedingung s3:prefix eines ListBucket weiter.
func ausS3(aktionen, ressourcen []string, cond map[string]map[string]json.RawMessage) (string, string) {
	var bucket, prefix string
	for _, r := range ressourcen {
		const kopf = "arn:aws:s3:::"
		if !strings.HasPrefix(r, kopf) {
			continue
		}
		rest := strings.TrimPrefix(r, kopf)
		name, schluessel, geteilt := strings.Cut(rest, "/")
		if name == "" || strings.ContainsAny(name, "*?") {
			continue // ein Platzhalter im Bucket-Namen taugt nicht als Vorschlag
		}
		if bucket == "" {
			bucket = name
		}
		if geteilt && len(ordner(schluessel)) > len(prefix) {
			prefix = ordner(schluessel)
		}
	}
	if prefix == "" {
		for _, w := range bedingung(cond, "s3:prefix") {
			if o := ordner(w); len(o) > len(prefix) {
				prefix = o
			}
		}
	}
	return bucket, prefix
}

// ordner macht aus einem Schluesselmuster wie "mail/ole/*" den Ordner "mail/ole/".
// Ein Muster mit Platzhalter mittendrin ("mail/*/posteingang") gibt nichts her.
func ordner(muster string) string {
	i := strings.IndexAny(muster, "*?")
	if i < 0 {
		i = len(muster)
	}
	p := muster[:i]
	if strings.ContainsAny(p, "*?") || p == "" {
		return ""
	}
	if !strings.HasSuffix(p, "/") {
		if j := strings.LastIndex(p, "/"); j >= 0 {
			p = p[:j+1] // "mail/ole" ist als Prefix nicht dasselbe wie "mail/"
		} else {
			return ""
		}
	}
	return p
}

// absenderAus liest die Adresse, auf die SES diesen Zugang festnagelt.
func absenderAus(cond map[string]map[string]json.RawMessage) string {
	for _, w := range bedingung(cond, "ses:FromAddress") {
		// Ein Muster wie "*@example.org" nennt keine Adresse, die man eintragen kann.
		if !strings.ContainsAny(w, "*?") && strings.Contains(w, "@") {
			return w
		}
	}
	return ""
}

// bedingung sammelt die Werte eines Bedingungsschluessels ueber alle Operatoren.
// IAM behandelt diese Schluessel ohne Ruecksicht auf Gross- und Kleinschreibung.
func bedingung(cond map[string]map[string]json.RawMessage, schluessel string) []string {
	var out []string
	for _, paare := range cond {
		for k, v := range paare {
			if strings.EqualFold(k, schluessel) {
				out = append(out, liste(v)...)
			}
		}
	}
	return out
}
