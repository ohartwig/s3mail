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

func newBodyCache(dir string) *bodyCache {
	if dir == "" {
		return nil
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	return &bodyCache{dir: dir}
}

func (c *bodyCache) path(etag string) string {
	// Das ETag kann Zeichen enthalten, die in einem Dateinamen nichts zu suchen
	// haben - also hashen statt hoffen.
	sum := sha256.Sum256([]byte(etag))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16])+".eml")
}

func (c *bodyCache) read(etag string) ([]byte, bool) {
	if c == nil || etag == "" {
		return nil, false
	}
	p := c.path(etag)
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

func (c *bodyCache) put(etag string, body []byte) {
	if c == nil || etag == "" || len(body) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tmp := c.path(etag) + ".tmp"
	if os.WriteFile(tmp, body, 0o600) != nil {
		return
	}
	if os.Rename(tmp, c.path(etag)) != nil {
		_ = os.Remove(tmp)
		return
	}
	c.cleanup()
}

// cleanup wirft die am laengsten unbenutzten Eintraege weg, bis die Grenze
// wieder eingehalten ist.
func (c *bodyCache) cleanup() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type file struct {
		path  string
		size  int64
		older int64
	}
	var all []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		all = append(all, file{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime().Unix()})
		total += info.Size()
	}
	if total <= CacheMax {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].older < all[j].older })
	for _, d := range all {
		if total <= CacheMax {
			return
		}
		if os.Remove(d.path) == nil {
			total -= d.size
		}
	}
}

// Clear wirft den ganzen Zwischenspeicher weg.
func (c *bodyCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = os.RemoveAll(c.dir)
	_ = os.MkdirAll(c.dir, 0o700)
}
