package awsx

import (
	"net/url"
	"testing"
)

// The real mailbox policy the way IAM delivers it: url-encoded, actions
// sometimes as a list, sometimes single. If the cut changes in koh-infra, it shows up here.
const mailboxPolicy = `{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid":"ListOwnPrefixOnly","Effect":"Allow","Action":"s3:ListBucket",
     "Resource":"arn:aws:s3:::koh-findready-mail",
     "Condition":{"StringLike":{"s3:prefix":"mail/ole/*"}}},
    {"Sid":"OwnMailbox","Effect":"Allow",
     "Action":["s3:PutObject","s3:GetObject","s3:DeleteObject"],
     "Resource":"arn:aws:s3:::koh-findready-mail/mail/ole/*"},
    {"Sid":"SendAsSelfOnly","Effect":"Allow","Action":["ses:SendRawEmail","ses:SendEmail"],
     "Resource":"*","Condition":{"StringEquals":{"ses:FromAddress":"ole@findready.ai"}}},
    {"Sid":"WizardConvenience","Effect":"Allow",
     "Action":["ses:ListIdentities","ses:GetIdentityVerificationAttributes"],"Resource":"*"}
  ]}`

func TestMailboxPolicyYieldsEverything(t *testing.T) {
	f := fromPolicies([]string{url.QueryEscape(mailboxPolicy)})
	if f.Bucket != "koh-findready-mail" {
		t.Errorf("bucket = %q", f.Bucket)
	}
	if f.Prefix != "mail/ole/" {
		t.Errorf("prefix = %q, expected mail/ole/", f.Prefix)
	}
	if f.From != "ole@findready.ai" {
		t.Errorf("sender = %q", f.From)
	}
	if !f.Complete() {
		t.Error("the finding counts as incomplete although bucket and prefix are there")
	}
}

func TestUnencodedDocumentWorksToo(t *testing.T) {
	// GetPolicyVersion delivers it encoded or not, depending on the path.
	if f := fromPolicies([]string{mailboxPolicy}); f.Prefix != "mail/ole/" {
		t.Errorf("Prefix = %q", f.Prefix)
	}
}

func TestTheMoreSpecificPrefixWins(t *testing.T) {
	// An administrator access may have the whole bucket and a mailbox all the same.
	doc := `{"Statement":[
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"},
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/mail/tim/*"}]}`
	f := fromPolicies([]string{doc})
	if f.Prefix != "mail/tim/" {
		t.Errorf("prefix = %q, expected the more precise one", f.Prefix)
	}
}

func TestDenyIsNotReadAsGuidance(t *testing.T) {
	// Otherwise the bucket policy, which forbids foreign mailboxes, would come
	// through as a suggestion - and the access would land in somebody else's mailbox.
	doc := `{"Statement":[{"Effect":"Deny","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/marc/*"}]}`
	if f := fromPolicies([]string{doc}); f.Bucket != "" || f.Prefix != "" {
		t.Errorf("Deny ausgewertet: %+v", f)
	}
}

func TestPlaceholdersAreNoSuggestion(t *testing.T) {
	doc := `{"Statement":[
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::*/mail/*"},
	 {"Effect":"Allow","Action":"ses:SendEmail","Resource":"*",
	  "Condition":{"StringLike":{"ses:FromAddress":"*@findready.ai"}}}]}`
	f := fromPolicies([]string{doc})
	if f.Bucket != "" {
		t.Errorf("bucket from a wildcard ARN: %q", f.Bucket)
	}
	if f.From != "" {
		t.Errorf("sender from a pattern: %q", f.From)
	}
}

func TestPrefixWithoutSlashIsCutToTheFolder(t *testing.T) {
	// "mail/ole" without a slash means something else to s3:prefix than
	// "mail/ole/" - and that is exactly what listing failed on at the first try.
	doc := `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/ole"}]}`
	if f := fromPolicies([]string{doc}); f.Prefix != "mail/" {
		t.Errorf("prefix = %q, expected mail/", f.Prefix)
	}
}

func TestSingleStatementWithoutList(t *testing.T) {
	doc := `{"Statement":{"Effect":"Allow","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/ole/*"}}`
	if f := fromPolicies([]string{doc}); f.Bucket != "b" || f.Prefix != "mail/ole/" {
		t.Errorf("%+v", f)
	}
}

func TestGarbageYieldsAnEmptyFinding(t *testing.T) {
	for _, d := range []string{"", "kein json", "{}", `{"Statement":42}`} {
		if f := fromPolicies([]string{d}); f.Bucket != "" || f.From != "" {
			t.Errorf("%q ergab %+v", d, f)
		}
	}
}

func TestUserFromArn(t *testing.T) {
	cases := map[string]string{
		"arn:aws:iam::1:user/koh-mail-ole":          "koh-mail-ole",
		"arn:aws:iam::1:user/team/mail/ole":         "ole",
		"arn:aws:iam::1:root":                       "",
		"arn:aws:sts::1:assumed-role/admin/sitzung": "",
		"": "",
	}
	for arn, want := range cases {
		if got := userFromArn(arn); got != want {
			t.Errorf("%q -> %q, expected %q", arn, got, want)
		}
	}
}

// TestTheQueueComesOutOfThePolicy - the same trick as bucket and prefix: what
// the access is allowed to do says where its mailbox is. One AWS call fewer,
// and one question fewer in the wizard.
func TestTheQueueComesOutOfThePolicy(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
	  {"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage"],
	   "Resource":"arn:aws:sqs:eu-north-1:123456789012:s3mail-ole"}]}`
	f := fromPolicies([]string{doc})
	want := "https://sqs.eu-north-1.amazonaws.com/123456789012/s3mail-ole"
	if f.Queue != want {
		t.Errorf("queue = %q, expected %q", f.Queue, want)
	}
}

// TestAWildcardQueueIsNoSuggestion - the same rule as for the bucket: a pattern
// names nothing anybody could hang on.
func TestAWildcardQueueIsNoSuggestion(t *testing.T) {
	doc := `{"Statement":[{"Effect":"Allow","Action":"sqs:ReceiveMessage",
	         "Resource":"arn:aws:sqs:eu-north-1:123456789012:*"}]}`
	if f := fromPolicies([]string{doc}); f.Queue != "" {
		t.Errorf("queue from a wildcard ARN: %q", f.Queue)
	}
}

// TestGarbageArnsYieldNothing - a policy is somebody else's text.
func TestGarbageArnsYieldNothing(t *testing.T) {
	for _, arn := range []string{"", "arn:aws:s3:::bucket", "arn:aws:sqs:::name",
		"nonsense", "arn:aws:sqs:eu-north-1:123456789012:"} {
		if got := QueueURL(arn); got != "" {
			t.Errorf("%q -> %q, expected nothing", arn, got)
		}
	}
}
