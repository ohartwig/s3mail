package awsx

import (
	"net/url"
	"testing"
)

// Die echte Postfach-Policy, wie IAM sie ausliefert: url-kodiert, Aktionen mal
// als Liste, mal einzeln. Aendert sich der Zuschnitt in koh-infra, faellt es hier auf.
const postfachPolicy = `{
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

func TestPostfachPolicyGibtAllesHer(t *testing.T) {
	f := ausPolicies([]string{url.QueryEscape(postfachPolicy)})
	if f.Bucket != "koh-findready-mail" {
		t.Errorf("Bucket = %q", f.Bucket)
	}
	if f.Prefix != "mail/ole/" {
		t.Errorf("Prefix = %q, erwartet mail/ole/", f.Prefix)
	}
	if f.Absender != "ole@findready.ai" {
		t.Errorf("Absender = %q", f.Absender)
	}
	if !f.Vollstaendig() {
		t.Error("Fund gilt als unvollstaendig, obwohl Bucket und Prefix da sind")
	}
}

func TestUnkodiertesDokumentGehtAuch(t *testing.T) {
	// GetPolicyVersion liefert je nach Weg kodiert oder nicht.
	if f := ausPolicies([]string{postfachPolicy}); f.Prefix != "mail/ole/" {
		t.Errorf("Prefix = %q", f.Prefix)
	}
}

func TestDerGenauerePrefixGewinnt(t *testing.T) {
	// Ein Verwalter-Zugang darf den ganzen Bucket und trotzdem ein Postfach.
	doc := `{"Statement":[
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"},
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/mail/tim/*"}]}`
	f := ausPolicies([]string{doc})
	if f.Prefix != "mail/tim/" {
		t.Errorf("Prefix = %q, erwartet den genaueren", f.Prefix)
	}
}

func TestDenyWirdNichtAlsHinweisGelesen(t *testing.T) {
	// Sonst schluege die Bucket-Policy, die fremde Postfaecher verbietet, als
	// Vorschlag durch - der Zugang landete im Postfach eines anderen.
	doc := `{"Statement":[{"Effect":"Deny","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/marc/*"}]}`
	if f := ausPolicies([]string{doc}); f.Bucket != "" || f.Prefix != "" {
		t.Errorf("Deny ausgewertet: %+v", f)
	}
}

func TestPlatzhalterTaugenNichtAlsVorschlag(t *testing.T) {
	doc := `{"Statement":[
	 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::*/mail/*"},
	 {"Effect":"Allow","Action":"ses:SendEmail","Resource":"*",
	  "Condition":{"StringLike":{"ses:FromAddress":"*@findready.ai"}}}]}`
	f := ausPolicies([]string{doc})
	if f.Bucket != "" {
		t.Errorf("Bucket aus Platzhalter-ARN: %q", f.Bucket)
	}
	if f.Absender != "" {
		t.Errorf("Absender aus Muster: %q", f.Absender)
	}
}

func TestPrefixOhneSchraegstrichWirdAufDenOrdnerGekuerzt(t *testing.T) {
	// "mail/ole" ohne Schraegstrich ist fuer s3:prefix etwas anderes als
	// "mail/ole/" - und genau daran scheiterte das Auflisten im ersten Versuch.
	doc := `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/ole"}]}`
	if f := ausPolicies([]string{doc}); f.Prefix != "mail/" {
		t.Errorf("Prefix = %q, erwartet mail/", f.Prefix)
	}
}

func TestEinzelnesStatementOhneListe(t *testing.T) {
	doc := `{"Statement":{"Effect":"Allow","Action":"s3:GetObject",
	 "Resource":"arn:aws:s3:::b/mail/ole/*"}}`
	if f := ausPolicies([]string{doc}); f.Bucket != "b" || f.Prefix != "mail/ole/" {
		t.Errorf("%+v", f)
	}
}

func TestMuellFuehrtZuLeeremFund(t *testing.T) {
	for _, d := range []string{"", "kein json", "{}", `{"Statement":42}`} {
		if f := ausPolicies([]string{d}); f.Bucket != "" || f.Absender != "" {
			t.Errorf("%q ergab %+v", d, f)
		}
	}
}

func TestBenutzerAusArn(t *testing.T) {
	faelle := map[string]string{
		"arn:aws:iam::1:user/koh-mail-ole":          "koh-mail-ole",
		"arn:aws:iam::1:user/team/mail/ole":         "ole",
		"arn:aws:iam::1:root":                       "",
		"arn:aws:sts::1:assumed-role/admin/sitzung": "",
		"": "",
	}
	for arn, will := range faelle {
		if got := benutzerAusArn(arn); got != will {
			t.Errorf("%q -> %q, erwartet %q", arn, got, will)
		}
	}
}
