// Package assistent bedient die /api/setup/*-Aufrufe: AWS-Zugang einrichten,
// Bucket waehlen, Verbindung pruefen, Papierkorb-Automatik setzen, speichern.
package assistent

import (
	"context"
	"fmt"
	"strings"

	"s3mail/awsx"
	"s3mail/i18n"
	"s3mail/konfig"
	"s3mail/pruefung"
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

// Daten ist, was die Assistentenseite schickt.
type Daten struct {
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

// Scharfschalten wird nach dem Speichern gerufen, damit der laufende Prozess ohne
// Neustart ins Postfach wechselt.
type Scharfschalten func(k konfig.Konfig) error

type Assistent struct {
	Aktivieren Scharfschalten
}

// Eingabefehler ist ein Fehler, den der Nutzer selbst beheben kann - die
// HTTP-Schicht macht daraus ein 400 statt eines 500.
type Eingabefehler struct{ Text string }

func (e Eingabefehler) Error() string { return e.Text }

func fehler(format string, a ...any) error {
	return Eingabefehler{fmt.Sprintf(format, a...)}
}

// Info liefert alles, was die Seite beim Laden braucht.
func (a *Assistent) Info(_ context.Context, _ Daten) (map[string]any, error) {
	regionen := make([]map[string]string, 0, len(Regionen))
	for _, r := range Regionen {
		regionen = append(regionen, map[string]string{"id": r[0], "label": r[1] + " · " + r[0]})
	}
	return map[string]any{
		"config":      konfig.Laden(),
		"profiles":    konfig.Profile(),
		"regions":     regionen,
		"config_file": konfig.Datei(),
	}, nil
}

// Zugangsdaten schreibt Access Key und Secret als benanntes AWS-Profil.
func (a *Assistent) Zugangsdaten(_ context.Context, d Daten) (map[string]any, error) {
	name, err := konfig.ZugangsdatenSchreiben(d.NeuesProfil, d.KeyID, d.Secret, d.Region)
	if err != nil {
		return nil, Eingabefehler{err.Error()}
	}
	return map[string]any{"profile": name, "profiles": konfig.Profile()}, nil
}

// Buckets fuellt die beiden Auswahllisten.
func (a *Assistent) Buckets(ctx context.Context, d Daten) (map[string]any, error) {
	cfg, err := awsx.Sitzung(ctx, d.Profil, d.Region)
	if err == nil {
		err = awsx.ZugangPruefen(ctx, cfg)
	}
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil, i18n.Get(d.Sprache))}
	}
	s3 := awsx.NeuS3(cfg, "")
	buckets, err := s3.Buckets(ctx)
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil, i18n.Get(d.Sprache))}
	}
	// Nie nil an die Oberflaeche geben: ein nil-Slice wird zu JSON `null`, und
	// `null.map(...)` beendet das Skript der Seite - der Nutzer sieht dann nicht
	// "kein Recht zum Auflisten", sondern gar nichts mehr.
	ses := awsx.NeuSES(cfg, "")
	out := map[string]any{
		"buckets":    nichtNil(buckets),
		"identities": nichtNil(ses.Identitaeten(ctx)),
		"domains":    nichtNil(ses.VerifizierteDomains(ctx)),
	}
	// Was der Zugang ueber sich selbst verraet, muss niemand abtippen. Scheitert
	// das (aeltere Postfaecher duerfen ihre Policy nicht lesen), bleibt der
	// Assistent bei der Handeingabe - deshalb hier kein Fehler nach aussen.
	if f, err := awsx.Erkunden(ctx, cfg); err == nil && (f.Bucket != "" || f.Absender != "") {
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
func (a *Assistent) Test(ctx context.Context, d Daten) (map[string]any, error) {
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, fehler("%s", i18n.Get(d.Sprache).T("setup.error.noBucket"))
	}
	cfg, err := awsx.Sitzung(ctx, d.Profil, d.Region)
	if err == nil {
		err = awsx.ZugangPruefen(ctx, cfg)
	}
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil, i18n.Get(d.Sprache))}
	}
	s3 := awsx.NeuS3(cfg, "")
	prefix := konfig.PrefixNormalisieren(d.Prefix)
	punkte := pruefung.Ausfuehren(ctx, s3, awsx.NeuKMS(cfg, ""), awsx.NeuSES(cfg, ""),
		d.Bucket, prefix, strings.TrimSpace(d.Absender), i18n.Get(d.Sprache))
	return map[string]any{
		"checks":         punkte,
		"ok":             pruefung.Alles(punkte),
		"lifecycle_days": s3.LifecycleTage(ctx, d.Bucket),
	}, nil
}

// Lifecycle setzt oder entfernt die Papierkorb-Automatik.
func (a *Assistent) Lifecycle(ctx context.Context, d Daten) (map[string]any, error) {
	cfg, err := awsx.Sitzung(ctx, d.Profil, d.Region)
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil, i18n.Get(d.Sprache))}
	}
	nachricht, err := awsx.NeuS3(cfg, "").LifecycleSetzen(ctx, d.Bucket,
		konfig.PrefixNormalisieren(d.Prefix), d.Tage)
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil, i18n.Get(d.Sprache))}
	}
	return map[string]any{"message": nachricht}, nil
}

// Speichern schreibt die Konfiguration und schaltet das Postfach scharf.
func (a *Assistent) Speichern(_ context.Context, d Daten) (map[string]any, error) {
	k := konfig.Laden()
	k.Profil, k.Region = d.Profil, d.Region
	k.Bucket = strings.TrimSpace(d.Bucket)
	k.Prefix = konfig.PrefixNormalisieren(d.Prefix)
	k.Absender = strings.TrimSpace(d.Absender)
	k.AllowDelete = d.AllowDelete == nil || *d.AllowDelete

	pfad, err := konfig.Speichern(k)
	if err != nil {
		return nil, Eingabefehler{err.Error()}
	}
	if a.Aktivieren != nil {
		if err := a.Aktivieren(k); err != nil {
			return nil, Eingabefehler{awsx.Klartext(err, k.Profil, i18n.Get(d.Sprache))}
		}
	}
	return map[string]any{"config": k, "path": pfad}, nil
}

// Route waehlt den Handler zum Pfad.
func (a *Assistent) Route(pfad string) (func(context.Context, Daten) (map[string]any, error), bool) {
	switch strings.TrimPrefix(pfad, "/api/setup/") {
	case "info":
		return a.Info, true
	case "credentials":
		return a.Zugangsdaten, true
	case "buckets":
		return a.Buckets, true
	case "test":
		return a.Test, true
	case "lifecycle":
		return a.Lifecycle, true
	case "save":
		return a.Speichern, true
	}
	return nil, false
}

func nichtNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
