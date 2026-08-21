package assistent

import (
	"encoding/json"
	"testing"
)

// TestNieNullAnDieOberflaeche haelt den Fehler fest, der v0.2.x fuer jeden
// Postfach-Benutzer unbrauchbar machte.
//
// Die Postfach-Policy gibt absichtlich kein s3:ListAllMyBuckets - sonst saehe
// jeder alle Buckets des Kontos, auch die Backups. Der Aufruf lief also in ein
// AccessDenied und lieferte ein nil-Slice. In Go wird daraus JSON `null`, nicht
// `[]`, und `null.map(...)` beendet das Skript der Seite. Der Nutzer sah dann
// nicht "kein Recht zum Auflisten", sondern gar nichts mehr:
//
//	✕ Cannot read properties of null (reading 'map')
func TestNieNullAnDieOberflaeche(t *testing.T) {
	for name, wert := range map[string][]string{
		"nil":  nil,
		"leer": {},
		"voll": {"a", "b"},
	} {
		blob, err := json.Marshal(map[string]any{"buckets": nichtNil(wert)})
		if err != nil {
			t.Fatal(err)
		}
		if string(blob) == `{"buckets":null}` {
			t.Errorf("%s: wird zu null - die Seite stirbt daran", name)
		}
	}
}

// TestNilWirdWirklichZuNull dokumentiert, warum es nichtNil ueberhaupt braucht -
// damit niemand die Funktion als ueberfluessig wegraeumt.
func TestNilWirdWirklichZuNull(t *testing.T) {
	var leer []string
	blob, _ := json.Marshal(map[string]any{"buckets": leer})
	if string(blob) != `{"buckets":null}` {
		t.Skip("Go marshalt nil-Slices nicht mehr als null - nichtNil kann weg")
	}
}
