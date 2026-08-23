package awsx

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Dieses Stueck ist absichtlich frei von AWS-Aufrufen: was in einem
// Policy-Dokument steht, laesst sich ohne Konto pruefen, und genau daran haengt,
// ob der Assistent das Richtige vorschlaegt.

// fromPolicies liest Bucket, Prefix und Absender aus IAM-Policy-Dokumenten.
func fromPolicies(documents []string) Finding {
	var f Finding
	for _, d := range documents {
		for _, s := range statements(d) {
			if !strings.EqualFold(s.Effect, "Allow") {
				continue // ein Deny sagt nichts darueber, wo das Postfach liegt
			}
			actions := list(s.Action)
			switch {
			case hasPrefix(actions, "s3:"):
				bucket, prefix := fromS3(actions, list(s.Resource), s.Condition)
				// Der laengste Prefix gewinnt: eine Policy darf den Bucket
				// grob und das eigene Postfach genau nennen.
				if bucket != "" && (f.Bucket == "" || len(prefix) > len(f.Prefix)) {
					f.Bucket, f.Prefix = bucket, prefix
				}
			case hasPrefix(actions, "ses:"):
				if a := senderFrom(s.Condition); a != "" {
					f.From = a
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
	var many []statement
	if json.Unmarshal(d.Statement, &many) == nil {
		return many
	}
	var first statement
	if json.Unmarshal(d.Statement, &first) == nil {
		return []statement{first}
	}
	return nil
}

// liste nimmt "s3:GetObject" genauso wie ["s3:GetObject", "s3:PutObject"].
func list(r json.RawMessage) []string {
	if len(r) == 0 {
		return nil
	}
	var many []string
	if json.Unmarshal(r, &many) == nil {
		return many
	}
	var first string
	if json.Unmarshal(r, &first) == nil {
		return []string{first}
	}
	return nil
}

func hasPrefix(values []string, p string) bool {
	for _, w := range values {
		if strings.HasPrefix(strings.ToLower(w), p) {
			return true
		}
	}
	return false
}

// fromS3 zieht Bucket und Prefix aus den Resource-ARNs einer S3-Erlaubnis.
// Fehlt der Prefix dort, hilft die Bedingung s3:prefix eines ListBucket weiter.
func fromS3(actions, resources []string, cond map[string]map[string]json.RawMessage) (string, string) {
	var bucket, prefix string
	for _, r := range resources {
		const header = "arn:aws:s3:::"
		if !strings.HasPrefix(r, header) {
			continue
		}
		rest := strings.TrimPrefix(r, header)
		name, key, shared := strings.Cut(rest, "/")
		if name == "" || strings.ContainsAny(name, "*?") {
			continue // ein Platzhalter im Bucket-Namen taugt nicht als Vorschlag
		}
		if bucket == "" {
			bucket = name
		}
		if shared && len(folderOf(key)) > len(prefix) {
			prefix = folderOf(key)
		}
	}
	if prefix == "" {
		for _, w := range condition(cond, "s3:prefix") {
			if o := folderOf(w); len(o) > len(prefix) {
				prefix = o
			}
		}
	}
	return bucket, prefix
}

// folderOf macht aus einem Schluesselmuster wie "mail/ole/*" den Ordner "mail/ole/".
// Ein Muster mit Platzhalter mittendrin ("mail/*/posteingang") gibt nichts her.
func folderOf(pattern string) string {
	i := strings.IndexAny(pattern, "*?")
	if i < 0 {
		i = len(pattern)
	}
	p := pattern[:i]
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

// senderFrom liest die Adresse, auf die SES diesen Zugang festnagelt.
func senderFrom(cond map[string]map[string]json.RawMessage) string {
	for _, w := range condition(cond, "ses:FromAddress") {
		// Ein Muster wie "*@example.org" nennt keine Adresse, die man eintragen kann.
		if !strings.ContainsAny(w, "*?") && strings.Contains(w, "@") {
			return w
		}
	}
	return ""
}

// condition sammelt die Werte eines Bedingungsschluessels ueber alle Operatoren.
// IAM behandelt diese Schluessel ohne Ruecksicht auf Gross- und Kleinschreibung.
func condition(cond map[string]map[string]json.RawMessage, key string) []string {
	var out []string
	for _, pairs := range cond {
		for k, v := range pairs {
			if strings.EqualFold(k, key) {
				out = append(out, list(v)...)
			}
		}
	}
	return out
}
