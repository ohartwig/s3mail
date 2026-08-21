package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// CacheMax ist die Obergrenze des Inhalts-Zwischenspeichers. Ein Postfach mit
// vielen Anhaengen soll die Platte nicht vollschreiben.
const CacheMax = 256 << 20 // 256 MB

// bodyCache haelt ganze Mails auf der Platte.
//
// Der Index liegt ohnehin schon lokal - deshalb kostet "Neu laden" bei
// unveraenderten Mails nichts. Der Inhalt tat es bisher nicht: eine Mail zum
// dritten Mal zu oeffnen hiess, sie zum dritten Mal aus S3 zu holen, samt
// Anhaengen.
//
// Geschluesselt wird ueber das ETag, nicht ueber den Key. Eine zugestellte Mail
// aendert sich nicht mehr, und beim Verschieben zwischen Ordnern wechselt der
// Key - der Inhalt aber nicht. So ueberlebt der Eintrag das Verschieben, und ein
// tatsaechlich veraendertes Objekt faellt automatisch heraus.
type bodyCache struct {
	dir string
	mu  sync.Mutex
}

func neuerBodyCache(dir string) *bodyCache {
	if dir == "" {
		return nil
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	return &bodyCache{dir: dir}
}

func (c *bodyCache) pfad(etag string) string {
	// Das ETag kann Zeichen enthalten, die in einem Dateinamen nichts zu suchen
	// haben - also hashen statt hoffen.
	sum := sha256.Sum256([]byte(etag))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16])+".eml")
}

func (c *bodyCache) lesen(etag string) ([]byte, bool) {
	if c == nil || etag == "" {
		return nil, false
	}
	p := c.pfad(etag)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	// Zugriffszeit anfassen, damit die Verdraengung die selten genutzten trifft
	// und nicht die zuletzt geschriebenen.
	n := time.Now()
	_ = os.Chtimes(p, n, n)
	return b, true
}

func (c *bodyCache) schreiben(etag string, body []byte) {
	if c == nil || etag == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tmp := c.pfad(etag) + ".tmp"
	if os.WriteFile(tmp, body, 0o600) != nil {
		return
	}
	if os.Rename(tmp, c.pfad(etag)) != nil {
		_ = os.Remove(tmp)
		return
	}
	c.aufraeumen()
}

// aufraeumen wirft die am laengsten unbenutzten Eintraege weg, bis die Grenze
// wieder eingehalten ist.
func (c *bodyCache) aufraeumen() {
	eintraege, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type datei struct {
		pfad    string
		groesse int64
		alter   int64
	}
	var alle []datei
	var summe int64
	for _, e := range eintraege {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		alle = append(alle, datei{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime().Unix()})
		summe += info.Size()
	}
	if summe <= CacheMax {
		return
	}
	sort.Slice(alle, func(i, j int) bool { return alle[i].alter < alle[j].alter })
	for _, d := range alle {
		if summe <= CacheMax {
			return
		}
		if os.Remove(d.pfad) == nil {
			summe -= d.groesse
		}
	}
}

// Leeren wirft den ganzen Zwischenspeicher weg.
func (c *bodyCache) Leeren() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.RemoveAll(c.dir)
	_ = os.MkdirAll(c.dir, 0o700)
}
