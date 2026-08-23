// Package awsx verbindet s3mail mit dem echten AWS: die Umsetzungen von store.S3
// und store.KMS, dazu die Uebersetzung der AWS-Fehler in Saetze, mit denen jemand
// etwas anfangen kann.
package awsx

import (
	"context"
	"errors"
	"path/filepath"
	"s3mail/i18n"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go"
)

// GeteiltesVerzeichnis ist der Ort von credentials und config. Leer heisst: was
// das SDK von sich aus nimmt (~/.aws).
//
// Der Assistent schreibt die Zugangsdaten irgendwohin - das SDK muss sie an
// derselben Stelle suchen, sonst legt jemand ein Profil an, das nie gefunden wird.
var GeteiltesVerzeichnis string

// Sitzung baut die AWS-Konfiguration aus Profil und Region. Beides darf leer sein;
// dann greift, was in der Umgebung steht.
func Sitzung(ctx context.Context, profil, region string) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error
	if profil != "" {
		opts = append(opts, config.WithSharedConfigProfile(profil))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	if GeteiltesVerzeichnis != "" {
		opts = append(opts,
			config.WithSharedCredentialsFiles([]string{
				filepath.Join(GeteiltesVerzeichnis, "credentials")}),
			config.WithSharedConfigFiles([]string{
				filepath.Join(GeteiltesVerzeichnis, "config")}))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ZugangPruefen holt die Zugangsdaten einmal ab. config.LoadDefaultConfig meldet
// naemlich noch keinen Fehler, wenn gar keine hinterlegt sind - das faellt sonst
// erst beim ersten Aufruf auf, mitten in einer anderen Operation.
func ZugangPruefen(ctx context.Context, cfg aws.Config) error {
	if cfg.Credentials == nil {
		return errors.New("keine Zugangsdaten")
	}
	_, err := cfg.Credentials.Retrieve(ctx)
	return err
}

// Klartext uebersetzt eine AWS-Ausnahme in einen Satz, der sagt, was zu tun ist.
//
// Das AWS-SDK meldet fehlende oder unbrauchbare Zugangsdaten in einem halben
// Dutzend Formen, alle englisch und alle ohne Hinweis darauf, dass der Assistent
// zwei Felder weiter oben genau das loesen wuerde.
func Klartext(err error, profil string, cat i18n.Catalog) string {
	if err == nil {
		return ""
	}
	wo := cat.T("aws.defaultProfile")
	if profil != "" {
		wo = cat.Tf("aws.namedProfile", profil)
	}
	keys := cat.T("aws.enterKeys")
	text := err.Error()

	// Profil gibt es nicht
	var fehltProfil config.SharedConfigProfileNotExistError
	if errors.As(err, &fehltProfil) {
		return cat.Tf("aws.noSuchProfile", profil, keys)
	}

	// SSO: die Anmeldung fehlt, nicht die Zugangsdaten
	if strings.Contains(text, "sso") || strings.Contains(text, "SSO") ||
		strings.Contains(text, "token") && strings.Contains(text, "expired") {
		befehl := "aws sso login"
		if profil != "" {
			befehl += " --profile " + profil
		}
		return cat.Tf("aws.ssoLoginNeeded", wo, befehl)
	}

	// Fehlende Region faellt sonst als kryptischer Endpunktfehler auf
	if strings.Contains(text, "no region") || strings.Contains(text, "region is required") ||
		strings.Contains(text, "MissingRegion") {
		return cat.T("aws.noRegion")
	}

	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvalidClientTokenId", "UnrecognizedClientException", "AuthFailure",
			"InvalidAccessKeyId":
			return cat.T("aws.badKeyId")
		case "SignatureDoesNotMatch":
			return cat.T("aws.badSecret")
		case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired":
			return cat.Tf("aws.expired", wo, keys)
		case "AccessDenied", "AccessDeniedException":
			return cat.T("aws.accessDenied")
		case "NoSuchBucket":
			return cat.T("aws.noSuchBucket")
		case "PermanentRedirect", "IllegalLocationConstraintException",
			"AuthorizationHeaderMalformed":
			return cat.T("aws.wrongRegion")
		}
	}

	// Keine Zugangsdaten - im SDK kommt das als CredentialRequiresARNError,
	// als leerer Provider oder schlicht als Text durch.
	if strings.Contains(text, "failed to refresh cached credentials") ||
		strings.Contains(text, "no EC2 IMDS role found") ||
		strings.Contains(text, "failed to retrieve credentials") ||
		strings.Contains(text, "keine Zugangsdaten") ||
		strings.Contains(text, "EmptyStaticCreds") {
		return cat.Tf("aws.noCredentials", wo, keys)
	}

	// Netz
	if strings.Contains(text, "no such host") || strings.Contains(text, "dial tcp") ||
		strings.Contains(text, "connection refused") || strings.Contains(text, "timeout") {
		return cat.T("aws.noConnection")
	}

	return text // Unbekanntes nicht verschlucken
}
