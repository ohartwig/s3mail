package wizard

import (
	"encoding/json"
	"testing"
)

// TestNeverNullToTheInterface pins down the bug that made v0.2.x unusable for
// every mailbox user.
//
// The mailbox policy deliberately grants no s3:ListAllMyBuckets - otherwise
// everyone would see every bucket of the account, backups included. So the
// call ran into an AccessDenied and returned a nil slice. In Go that becomes
// JSON `null`, not `[]`, and `null.map(...)` ends the page's script. The
// reader then saw not "no right to list" but nothing at all any more:
//
//	✕ Cannot read properties of null (reading 'map')
func TestNeverNullToTheInterface(t *testing.T) {
	for name, value := range map[string][]string{
		"nil":  nil,
		"leer": {},
		"voll": {"a", "b"},
	} {
		blob, err := json.Marshal(map[string]any{"buckets": notNil(value)})
		if err != nil {
			t.Fatal(err)
		}
		if string(blob) == `{"buckets":null}` {
			t.Errorf("%s: becomes null - the page dies on it", name)
		}
	}
}

// TestNilReallyBecomesNull documents why notNil is needed at all - so nobody
// clears the function away as superfluous.
func TestNilReallyBecomesNull(t *testing.T) {
	var empty []string
	blob, _ := json.Marshal(map[string]any{"buckets": empty})
	if string(blob) != `{"buckets":null}` {
		t.Skip("Go marshalt nil-Slices nicht mehr als null - nichtNil kann weg")
	}
}
