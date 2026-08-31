// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"encoding/json"
	"net/url"
	"strings"
)

// This piece is deliberately free of AWS calls: what a policy document says can
// be checked without an account, and whether the wizard suggests the right
// thing hangs on exactly that.

// fromPolicies reads bucket, prefix, sender and queue from IAM policy documents.
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
			case hasPrefix(actions, "sns:"):
				// Two different things live under sns:, and they are told
				// apart by the action rather than by the ARN - an ARN can be
				// mistyped, an action cannot.
				for _, a := range actions {
					switch a {
					case "sns:Subscribe":
						for _, arn := range list(s.Resource) {
							if i := strings.LastIndex(arn, ":"); i > 0 {
								f.PushTopic = arn
							}
						}
					}
				}
			case hasPrefix(actions, "iam:"):
				// The boundary a device user must carry, out of the condition
				// that makes creating one safe. Its presence is also the
				// answer to "may this access create devices at all".
				if b := condition(s.Condition, "iam:PermissionsBoundary"); len(b) > 0 {
					f.DeviceBoundary = b[0]
				}
			case hasPrefix(actions, "sqs:"):
				// The queue the mailbox is allowed to receive from is the one it
				// should hang on. Whoever may read it was meant to be told.
				for _, arn := range list(s.Resource) {
					if url := QueueURL(arn); url != "" {
						f.Queue = url
					}
				}
			}
		}
	}
	// The local part is the last segment of the prefix: "mail/ole/" -> "ole".
	// Derived and not asked for, like everything else here.
	f.Mailbox = mailboxOf(f.Prefix)
	return f
}

// mailboxOf takes the local part out of a prefix.
func mailboxOf(prefix string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return ""
	}
	if _, after, ok := strings.CutLast(p, "/"); ok {
		return after
	}
	return p
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
