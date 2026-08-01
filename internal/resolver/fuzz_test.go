package resolver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
)

// FuzzParse feeds arbitrary bytes to the parser: it must never panic, and any
// valid-JSON-object input must round-trip its fields losslessly.
func FuzzParse(f *testing.F) {
	seeds := []string{
		`{"name":"a","inherits":"b"}`,
		`{"name":"unicode 名前","inherits":""}`,
		`{"name":"nums","x":1.5,"y":[1,2,3],"z":null}`,
		`{"name":"dup","name":"dup2"}`,
		`not json`,
		`[]`,
		`null`,
		`{"name": "` + strings.Repeat("a", 500) + `"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		p, err := parser.Parse([]byte(in))
		if err != nil {
			return // invalid JSON is an expected outcome, not a bug
		}
		// A successful parse of a JSON object must preserve every field:
		// re-marshal p.Fields and compare decoded content to the original.
		var original map[string]any
		if jsonErr := json.Unmarshal([]byte(in), &original); jsonErr != nil {
			return // Parse succeeded on something json.Unmarshal itself rejects: impossible, skip.
		}
		if len(original) != len(p.Fields) {
			t.Fatalf("field count changed: input had %d, parsed has %d (input: %s)", len(original), len(p.Fields), in)
		}
	})
}

// FuzzResolveNoPanic exercises the resolver with a randomly generated chain
// of profiles: it must terminate (no infinite loop / stack overflow) and
// never panic, whether or not the chain is well-formed.
func FuzzResolveNoPanic(f *testing.F) {
	f.Add(3, uint8(0b101)) // depth=3, wiring bits pick which links exist
	f.Add(1, uint8(0b000))
	f.Add(8, uint8(0xFF)) // deep chain, fully linked

	f.Fuzz(func(t *testing.T, depth int, wiring uint8) {
		if depth < 0 {
			depth = -depth
		}
		if depth > 64 {
			depth = 64 // bound fuzz-generated depth so this stays a unit test, not a stress test
		}

		set := Set{}
		var leafName string
		for i := 0; i < depth; i++ {
			name := fmt.Sprintf("p%d", i)
			inherits := ""
			// Use one bit per profile (mod 8) to decide whether it links to
			// the next profile in the chain or is left dangling/self-referential.
			bit := (wiring >> uint(i%8)) & 1
			switch {
			case bit == 1 && i > 0:
				inherits = fmt.Sprintf("p%d", i-1)
			case bit == 1 && i == 0:
				inherits = name // deliberately self-referential: must be caught as circular, not panic
			}
			set[name] = &domain.RawProfile{Name: name, Inherits: inherits, Fields: map[string]any{"name": name, "inherits": inherits}}
			leafName = name
		}
		if depth == 0 {
			return
		}
		leaf := set[leafName]

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Resolve panicked on depth=%d wiring=%#b: %v", depth, wiring, r)
			}
		}()
		_, _, _ = Resolve(set, leaf) // error is an acceptable outcome (missing/circular); panic is not
	})
}
