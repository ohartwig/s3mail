// Package awsx verbindet s3mail mit dem echten AWS: die Umsetzungen von store.S3
// und store.KMS, dazu die Uebersetzung der AWS-Fehler in Saetze, mit denen jemand
// etwas anfangen kann.
package awsx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
func Klartext(err error, profil string) string {
	if err == nil {
		return ""
	}
	wo := "das Standardprofil"
	if profil != "" {
		wo = fmt.Sprintf("Profil „%s“", profil)
	}
	keys := "Trag in Schritt 1 unter „Neue Zugangsdaten“ Access Key ID und Secret " +
		"ein und klick auf „Zugangsdaten speichern“."
	text := err.Error()

	// Profil gibt es nicht
	var fehltProfil config.SharedConfigProfileNotExistError
	if errors.As(err, &fehltProfil) {
		return fmt.Sprintf("Das AWS-Profil „%s“ gibt es auf diesem Rechner nicht. "+
			"Wähle ein anderes – oder leg eins an: %s", profil, keys)
	}

	// SSO: die Anmeldung fehlt, nicht die Zugangsdaten
	if strings.Contains(text, "sso") || strings.Contains(text, "SSO") ||
		strings.Contains(text, "token") && strings.Contains(text, "expired") {
		befehl := "aws sso login"
		if profil != "" {
			befehl += " --profile " + profil
		}
		return fmt.Sprintf("%s meldet sich über AWS SSO an, aber es liegt keine gültige "+
			"Anmeldung vor. Einmal „%s“ im Terminal ausführen, dann hier noch einmal "+
			"auf „Buckets laden“.", wo, befehl)
	}

	// Fehlende Region faellt sonst als kryptischer Endpunktfehler auf
	if strings.Contains(text, "no region") || strings.Contains(text, "region is required") ||
		strings.Contains(text, "MissingRegion") {
		return "Es ist keine Region gesetzt. Wähle sie in Schritt 1 aus."
	}

	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "InvalidClientTokenId", "UnrecognizedClientException", "AuthFailure",
			"InvalidAccessKeyId":
			return "Die Access Key ID stimmt nicht. Bitte in Schritt 1 noch einmal prüfen."
		case "SignatureDoesNotMatch":
			return "Der Secret Access Key stimmt nicht. Bitte in Schritt 1 noch einmal " +
				"eintragen – beim Kopieren geht gern ein Zeichen verloren."
		case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired":
			return fmt.Sprintf("Die Zugangsdaten für %s sind abgelaufen. %s", wo, keys)
		case "AccessDenied", "AccessDeniedException":
			return "Die Zugangsdaten stimmen, aber ihnen fehlt ein Recht. Welches, sagt " +
				"der Verbindungstest in Schritt 3 im Klartext."
		case "NoSuchBucket":
			return "Diesen Bucket gibt es nicht – Name oder Region stimmen nicht."
		case "PermanentRedirect", "IllegalLocationConstraintException",
			"AuthorizationHeaderMalformed":
			return "Der Bucket liegt in einer anderen Region. Region oben umstellen."
		}
	}

	// Keine Zugangsdaten - im SDK kommt das als CredentialRequiresARNError,
	// als leerer Provider oder schlicht als Text durch.
	if strings.Contains(text, "failed to refresh cached credentials") ||
		strings.Contains(text, "no EC2 IMDS role found") ||
		strings.Contains(text, "failed to retrieve credentials") ||
		strings.Contains(text, "keine Zugangsdaten") ||
		strings.Contains(text, "EmptyStaticCreds") {
		return fmt.Sprintf("Für %s sind keine brauchbaren Zugangsdaten hinterlegt. %s", wo, keys)
	}

	// Netz
	if strings.Contains(text, "no such host") || strings.Contains(text, "dial tcp") ||
		strings.Contains(text, "connection refused") || strings.Contains(text, "timeout") {
		return "Keine Verbindung zu AWS. Internet erreichbar? Proxy dazwischen?"
	}

	return text // Unbekanntes nicht verschlucken
}
