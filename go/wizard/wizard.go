// Package assistent bedient die /api/setup/*-Aufrufe: AWS-Zugang einrichten,
// Bucket waehlen, Verbindung pruefen, Papierkorb-Automatik setzen, speichern.
package wizard

import (
	"context"
	"fmt"
	"strings"

	"s3mail/awsx"
	"s3mail/check"
	"s3mail/config"
	"s3mail/i18n"
)

// Regionen, in denen SES eingehende Mail entgegennimmt. Andere anzubieten waere
// eine Falle - der Bucket laesst sich ueberall anlegen, die Receipt-Rule nicht.
//
// Die Namen stehen englisch da und werden nicht uebersetzt: so heissen sie bei
// AWS, und wer sie in der Konsole wiederfinden will, sucht nach genau diesem
// Wort.
var Regionen = [][2]string{
	{"eu-central-1", "Europe (Frankfurt)"}, {"eu-west-1", "Europe (Ireland)"},
	{"eu-west-2", "Europe (London)"}, {"eu-west-3", "Europe (Paris)"},
	{"eu-north-1", "Europe (Stockholm)"}, {"eu-south-1", "Europe (Milan)"},
	{"us-east-1", "US East (N. Virginia)"}, {"us-east-2", "US East (Ohio)"},
	{"us-west-1", "US West (N. California)"}, {"us-west-2", "US West (Oregon)"},
	{"ca-central-1", "Canada (Central)"}, {"sa-east-1", "South America (São Paulo)"},
	{"ap-northeast-1", "Asia Pacific (Tokyo)"}, {"ap-northeast-2", "Asia Pacific (Seoul)"},
	{"ap-southeast-1", "Asia Pacific (Singapore)"}, {"ap-southeast-2", "Asia Pacific (Sydney)"},
	{"ap-south-1", "Asia Pacific (Mumbai)"}, {"il-central-1", "Israel (Tel Aviv)"},
	{"af-south-1", "Africa (Cape Town)"}, {"me-south-1", "Middle East (Bahrain)"},
}

// Data ist, was die Assistentenseite schickt.
type Data struct {
	Profil      string `json:"profile"`
	NeuesProfil string `json:"new_profile"`
	KeyID       string `json:"key_id"`
	Secret      string `json:"secret"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	Absender    string `json:"from"`
	AllowDelete *bool  `json:"allow_delete"`
	Tage        int    `json:"days"`

	// Sprache setzt die HTTP-Schicht aus der Anfrage, nicht der Browser aus
	// dem Formular: sie steht dort schon im Cookie, und zwei Quellen fuer
	// dieselbe Angabe gehen irgendwann auseinander.
	Sprache string `json:"-"`
}

// Activator wird nach dem Speichern gerufen, damit der laufende Prozess ohne
// Neustart ins Postfach wechselt.
type Activator func(k config.Config) error

type Wizard struct {
	Aktivieren Activator
}

// InputError ist ein Fehler, den der Nutzer selbst beheben kann - die
// HTTP-Schicht macht daraus ein 400 statt eines 500.
type InputError struct{ Text string }

func (e InputError) Error() string { return e.Text }

func inputError(format string, a ...any) error {
	return InputError{fmt.Sprintf(format, a...)}
}

// Info liefert alles, was die Seite beim Laden braucht.
func (a *Wizard) Info(_ context.Context, _ Data) (map[string]any, error) {
	regionen := make([]map[string]string, 0, len(Regionen))
	for _, r := range Regionen {
		regionen = append(regionen, map[string]string{"id": r[0], "label": r[1] + " · " + r[0]})
	}
	return map[string]any{
		"config":      config.Load(),
		"profiles":    config.Profiles(),
		"regions":     regionen,
		"config_file": config.File(),
	}, nil
}

// Credentials schreibt Access Key und Secret als benanntes AWS-Profil.
func (a *Wizard) Credentials(_ context.Context, d Data) (map[string]any, error) {
	name, err := config.WriteCredentials(d.NeuesProfil, d.KeyID, d.Secret, d.Region)
	if err != nil {
		return nil, InputError{err.Error()}
	}
	return map[string]any{"profile": name, "profiles": config.Profiles()}, nil
}

// Buckets fuellt die beiden Auswahllisten.
func (a *Wizard) Buckets(ctx context.Context, d Data) (map[string]any, error) {
	cfg, err := awsx.Session(ctx, d.Profil, d.Region)
	if err == nil {
		err = awsx.CheckAccess(ctx, cfg)
	}
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profil, i18n.Get(d.Sprache))}
	}
	s3 := awsx.NewS3(cfg, "")
	buckets, err := s3.Buckets(ctx)
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profil, i18n.Get(d.Sprache))}
	}
	// Nie nil an die Oberflaeche geben: ein nil-Slice wird zu JSON `null`, und
	// `null.map(...)` beendet das Skript der Seite - der Nutzer sieht dann nicht
	// "kein Recht zum Auflisten", sondern gar nichts mehr.
	ses := awsx.NewSES(cfg, "")
	out := map[string]any{
		"buckets":    notNil(buckets),
		"identities": notNil(ses.Identitaeten(ctx)),
		"domains":    notNil(ses.VerifizierteDomains(ctx)),
	}
	// Was der Zugang ueber sich selbst verraet, muss niemand abtippen. Scheitert
	// das (aeltere Postfaecher duerfen ihre Policy nicht lesen), bleibt der
	// Assistent bei der Handeingabe - deshalb hier kein Fehler nach aussen.
	if f, err := awsx.Discover(ctx, cfg); err == nil && (f.Bucket != "" || f.Absender != "") {
		out["found"] = f
	}
	if len(buckets) == 0 {
		// Fuer die Postfach-Benutzer ist das der Normalfall und kein Mangel: ihre
		// Policy gibt bewusst kein s3:ListAllMyBuckets, sonst saehe jeder alle
		// Buckets des Kontos. Der Text fuehrt deshalb mit dem, was zu tun ist,
		// und nicht mit dem, was fehlt.
		out["note"] = i18n.Get(d.Sprache).T("setup.note.noListBuckets")
	}
	return out, nil
}

// Test ist Schritt 3: die Checkliste.
func (a *Wizard) Test(ctx context.Context, d Data) (map[string]any, error) {
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, inputError("%s", i18n.Get(d.Sprache).T("setup.error.noBucket"))
	}
	cfg, err := awsx.Session(ctx, d.Profil, d.Region)
	if err == nil {
		err = awsx.CheckAccess(ctx, cfg)
	}
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profil, i18n.Get(d.Sprache))}
	}
	s3 := awsx.NewS3(cfg, "")
	prefix := config.NormalizePrefix(d.Prefix)
	items := check.Run(ctx, s3, awsx.NewKMS(cfg, ""), awsx.NewSES(cfg, ""),
		d.Bucket, prefix, strings.TrimSpace(d.Absender), i18n.Get(d.Sprache))
	return map[string]any{
		"checks":         items,
		"ok":             check.AllOK(items),
		"lifecycle_days": s3.LifecycleTage(ctx, d.Bucket),
	}, nil
}

// Lifecycle setzt oder entfernt die Papierkorb-Automatik.
func (a *Wizard) Lifecycle(ctx context.Context, d Data) (map[string]any, error) {
	cfg, err := awsx.Session(ctx, d.Profil, d.Region)
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profil, i18n.Get(d.Sprache))}
	}
	msg, err := awsx.NewS3(cfg, "").LifecycleSetzen(ctx, d.Bucket,
		config.NormalizePrefix(d.Prefix), d.Tage)
	if err != nil {
		return nil, InputError{awsx.PlainText(err, d.Profil, i18n.Get(d.Sprache))}
	}
	return map[string]any{"message": msg}, nil
}

// Save schreibt die Konfiguration und schaltet das Postfach scharf.
func (a *Wizard) Save(_ context.Context, d Data) (map[string]any, error) {
	k := config.Load()
	k.Profil, k.Region = d.Profil, d.Region
	k.Bucket = strings.TrimSpace(d.Bucket)
	k.Prefix = config.NormalizePrefix(d.Prefix)
	k.Absender = strings.TrimSpace(d.Absender)
	k.AllowDelete = d.AllowDelete == nil || *d.AllowDelete

	path, err := config.Save(k)
	if err != nil {
		return nil, InputError{err.Error()}
	}
	if a.Aktivieren != nil {
		if err := a.Aktivieren(k); err != nil {
			return nil, InputError{awsx.PlainText(err, k.Profil, i18n.Get(d.Sprache))}
		}
	}
	return map[string]any{"config": k, "path": path}, nil
}

// Route waehlt den Handler zum Pfad.
func (a *Wizard) Route(path string) (func(context.Context, Data) (map[string]any, error), bool) {
	switch strings.TrimPrefix(path, "/api/setup/") {
	case "info":
		return a.Info, true
	case "credentials":
		return a.Credentials, true
	case "buckets":
		return a.Buckets, true
	case "test":
		return a.Test, true
	case "lifecycle":
		return a.Lifecycle, true
	case "save":
		return a.Save, true
	}
	return nil, false
}

func notNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
