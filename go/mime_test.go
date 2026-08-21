package mimespike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// TestGegenPython faehrt jede Mail aus dem Korpus durch den Go-Parser und
// vergleicht Feld fuer Feld mit dem, was der bestehende Python-Parser liefert.
func TestGegenPython(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]Summary
	if err := json.Unmarshal(blob, &want); err != nil {
		t.Fatal(err)
	}
	fallback := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", "corpus", name+".eml"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := Summarize(raw, fallback)
		w := want[name]

		cmp := func(feld, a, b string) {
			if a == b {
				return
			}
			if grund, gewollt := abweichungen[name+"/"+feld]; gewollt {
				t.Logf("%s / %s: bewusst anders (%s)\n  python: %q\n  go:     %q",
					name, feld, grund, b, a)
				return
			}
			t.Errorf("%s / %s:\n  python: %q\n  go:     %q", name, feld, b, a)
		}
		cmp("subject", got.Subject, w.Subject)
		cmp("from", got.From, w.From)
		cmp("to", got.To, w.To)
		cmp("cc", got.Cc, w.Cc)
		cmp("preview", got.Preview, w.Preview)
		cmp("spam", got.Spam, w.Spam)
		if got.HasHTML != w.HasHTML {
			t.Errorf("%s / has_html: python=%v go=%v", name, w.HasHTML, got.HasHTML)
		}
		if len(got.Attachments) != len(w.Attachments) {
			if grund, gewollt := abweichungen[name+"/anhaenge"]; gewollt {
				t.Logf("%s / anhaenge: bewusst anders (%s) python=%v go=%v",
					name, grund, namen(w.Attachments), namen(got.Attachments))
				continue
			}
			t.Errorf("%s / anhaenge: python=%d %v, go=%d %v",
				name, len(w.Attachments), namen(w.Attachments),
				len(got.Attachments), namen(got.Attachments))
			continue
		}
		for i := range got.Attachments {
			cmp("anhang-name", got.Attachments[i].Name, w.Attachments[i].Name)
			cmp("anhang-ctype", got.Attachments[i].CType, w.Attachments[i].CType)
		}
	}
}

// abweichungen sind die Stellen, an denen der Go-Parser bewusst etwas anderes
// liefert als der Python-Parser - allesamt, weil Python dort Daten verliert.
// Jede Zeile hier ist eine Entscheidung, keine Nachlaessigkeit.
var abweichungen = map[string]string{
	"03-cp1252-8bit/subject": "roher 8-Bit-Header wird als windows-1252 gerettet; " +
		"Python setzt Ersatzzeichen, der Text ist dort verloren",
	"08-inline-cid/anhaenge": "Bild, auf das das HTML per cid: zeigt, ist kein Anhang; " +
		"Python haengt an jede Signatur mit Logo eine Bueroklammer",
	"10-lying-charset/preview": "Body ist UTF-8, obwohl us-ascii deklariert ist - " +
		"Go erkennt das, Python zerlegt die Umlaute",
}

func namen(a []Attachment) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.Name
	}
	return out
}
