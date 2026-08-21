// Package assistent bedient die /api/setup/*-Aufrufe: AWS-Zugang einrichten,
// Bucket waehlen, Verbindung pruefen, Papierkorb-Automatik setzen, speichern.
package assistent

import (
	"context"
	"fmt"
	"strings"

	"s3mail/awsx"
	"s3mail/konfig"
	"s3mail/pruefung"
)

// Regionen, in denen SES eingehende Mail entgegennimmt. Andere anzubieten waere
// eine Falle - der Bucket laesst sich ueberall anlegen, die Receipt-Rule nicht.
var Regionen = [][2]string{
	{"eu-central-1", "Europa (Frankfurt)"}, {"eu-west-1", "Europa (Irland)"},
	{"eu-west-2", "Europa (London)"}, {"eu-west-3", "Europa (Paris)"},
	{"eu-north-1", "Europa (Stockholm)"}, {"eu-south-1", "Europa (Mailand)"},
	{"us-east-1", "USA Ost (N. Virginia)"}, {"us-east-2", "USA Ost (Ohio)"},
	{"us-west-1", "USA West (N. Kalifornien)"}, {"us-west-2", "USA West (Oregon)"},
	{"ca-central-1", "Kanada (Zentral)"}, {"sa-east-1", "Südamerika (São Paulo)"},
	{"ap-northeast-1", "Asien-Pazifik (Tokio)"}, {"ap-northeast-2", "Asien-Pazifik (Seoul)"},
	{"ap-southeast-1", "Asien-Pazifik (Singapur)"}, {"ap-southeast-2", "Asien-Pazifik (Sydney)"},
	{"ap-south-1", "Asien-Pazifik (Mumbai)"}, {"il-central-1", "Israel (Tel Aviv)"},
	{"af-south-1", "Afrika (Kapstadt)"}, {"me-south-1", "Naher Osten (Bahrain)"},
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
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil)}
	}
	s3 := awsx.NeuS3(cfg, "")
	buckets, err := s3.Buckets(ctx)
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil)}
	}
	// Nie nil an die Oberflaeche geben: ein nil-Slice wird zu JSON `null`, und
	// `null.map(...)` beendet das Skript der Seite - der Nutzer sieht dann nicht
	// "kein Recht zum Auflisten", sondern gar nichts mehr.
	out := map[string]any{
		"buckets":    nichtNil(buckets),
		"identities": nichtNil(awsx.NeuSES(cfg, "").Identitaeten(ctx)),
	}
	if len(buckets) == 0 {
		out["note"] = "Kein Recht zum Auflisten aller Buckets (s3:ListAllMyBuckets) – " +
			"Namen bitte direkt eintippen."
	}
	return out, nil
}

// Test ist Schritt 3: die Checkliste.
func (a *Assistent) Test(ctx context.Context, d Daten) (map[string]any, error) {
	if strings.TrimSpace(d.Bucket) == "" {
		return nil, fehler("Bitte einen Bucket angeben")
	}
	cfg, err := awsx.Sitzung(ctx, d.Profil, d.Region)
	if err == nil {
		err = awsx.ZugangPruefen(ctx, cfg)
	}
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil)}
	}
	s3 := awsx.NeuS3(cfg, "")
	prefix := konfig.PrefixNormalisieren(d.Prefix)
	punkte := pruefung.Ausfuehren(ctx, s3, awsx.NeuKMS(cfg, ""), awsx.NeuSES(cfg, ""),
		d.Bucket, prefix, strings.TrimSpace(d.Absender))
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
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil)}
	}
	nachricht, err := awsx.NeuS3(cfg, "").LifecycleSetzen(ctx, d.Bucket,
		konfig.PrefixNormalisieren(d.Prefix), d.Tage)
	if err != nil {
		return nil, Eingabefehler{awsx.Klartext(err, d.Profil)}
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
			return nil, Eingabefehler{awsx.Klartext(err, k.Profil)}
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
