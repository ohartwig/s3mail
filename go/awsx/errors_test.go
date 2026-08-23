package awsx_test

import (
	"errors"
	"strings"
	"testing"

	"s3mail/i18n"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go"

	"s3mail/awsx"
)

func apiError(code string) error {
	return &smithy.OperationError{
		ServiceID: "S3", OperationName: "ListBuckets",
		Err: &smithy.GenericAPIError{Code: code, Message: "irgendwas"},
	}
}

// TestPlainText - das AWS-SDK meldet fehlende Zugangsdaten in einem halben Dutzend
// Formen, alle englisch. Daraus muss ein Satz mit dem naechsten Schritt werden.
func TestPlainText(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		profile string
		must    []string
		mustNot []string
	}{
		{"kein Profil", config.SharedConfigProfileNotExistError{}, "gibtsnicht",
			[]string{"gibtsnicht", "gibt es auf diesem Rechner nicht"}, nil},
		{"keine Zugangsdaten", errors.New("failed to refresh cached credentials"), "",
			[]string{"Standardprofil", "Zugangsdaten speichern"},
			[]string{"failed to refresh"}},
		{"SSO nicht angemeldet", errors.New("sso session token expired"), "arbeit",
			[]string{"aws sso login --profile arbeit"}, []string{"Access Key ID und Secret ein"}},
		{"falsche Key-ID", apiError("InvalidClientTokenId"), "",
			[]string{"Access Key ID stimmt nicht"}, nil},
		{"falsches Secret", apiError("SignatureDoesNotMatch"), "",
			[]string{"Secret Access Key"}, nil},
		{"abgelaufen", apiError("ExpiredToken"), "s3mail",
			[]string{"s3mail", "abgelaufen"}, nil},
		{"kein Recht", apiError("AccessDenied"), "",
			[]string{"fehlt ein Recht", "Verbindungstest"}, nil},
		{"Bucket weg", apiError("NoSuchBucket"), "",
			[]string{"gibt es nicht"}, nil},
		{"falsche Region", apiError("PermanentRedirect"), "",
			[]string{"anderen Region"}, nil},
		{"keine Region", errors.New("an AWS region is required"), "",
			[]string{"keine Region gesetzt"}, nil},
		{"kein Netz", errors.New("dial tcp: lookup s3.eu-central-1.amazonaws.com: no such host"), "",
			[]string{"Keine Verbindung zu AWS"}, nil},
	}
	for _, f := range cases {
		got := awsx.PlainText(f.err, f.profile, i18n.Get("de"))
		for _, part := range f.must {
			if !strings.Contains(got, part) {
				t.Errorf("%s: %q enthaelt nicht %q", f.name, got, part)
			}
		}
		for _, part := range f.mustNot {
			if strings.Contains(got, part) {
				t.Errorf("%s: %q sollte %q nicht enthalten", f.name, got, part)
			}
		}
	}
}

// TestPlainTextSwallowsNothing - was wir nicht kennen, muss durchkommen, sonst
// steht der Nutzer vor einer freundlichen, aber nutzlosen Meldung.
func TestPlainTextSwallowsNothing(t *testing.T) {
	if got := awsx.PlainText(errors.New("Boom aus dem Nichts"), "", i18n.Get("de")); !strings.Contains(got, "Boom") {
		t.Errorf("unbekannter Fehler verschluckt: %q", got)
	}
	if awsx.PlainText(nil, "", i18n.Get("de")) != "" {
		t.Error("nil ergibt eine Meldung")
	}
}
