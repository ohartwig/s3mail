package awsx

import (
	"encoding/json"
	"net/url"
	"strings"
)

// This piece is deliberately free of AWS calls: what a policy document says can
// be checked without an account, and whether the wizard suggests the right
// thing hangs on exactly that.

// fromPolicies reads bucket, prefix and sender from IAM policy documents.
func fromPolicies(documents []string) Finding {
	var f Finding
	for _, d := range documents {
		for _, s := range statements(d) {
			if !strings.EqualFold(s.Effect, "Allow") {
				continue // a Deny says nothing about where the mailbox lies
			}
			actions := list(s.Action)
			switch {
			case hasPrefix(actions, "s3:"):
				bucket, prefix := fromS3(actions, list(s.Resource), s.Condition)
				// The longest prefix wins: a policy may name the bucket coarsely and the
				// mailbox itself precisely.
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

// statements unpacks a policy document. IAM delivers it url-encoded, and
// "Statement" may be a single object or a list.
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

// fromS3 pulls bucket and prefix out of the resource ARNs of an S3 permission.
// If the prefix is missing there, the s3:prefix condition of a ListBucket helps.
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
			continue // a wildcard in the bucket name is no use as a suggestion
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

// folderOf turns a key pattern like "mail/ole/*" into the folder "mail/ole/".
// A pattern with a wildcard in the middle ("mail/*/inbox") yields nothing.
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
			p = p[:j+1] // "mail/ole" as a prefix is not the same as "mail/"
		} else {
			return ""
		}
	}
	return p
}

// senderFrom reads the address SES pins this access to.
func senderFrom(cond map[string]map[string]json.RawMessage) string {
	for _, w := range condition(cond, "ses:FromAddress") {
		// A pattern like "*@example.org" names no address anybody could enter.
		if !strings.ContainsAny(w, "*?") && strings.Contains(w, "@") {
			return w
		}
	}
	return ""
}

// condition collects the values of a condition key across all operators.
// IAM treats these keys without regard to case.
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
