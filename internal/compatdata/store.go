package compatdata

import (
	"crypto/ed25519"
	"encoding/json"
	"os"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// Store is the last-known-good profile document.
type Store struct {
	path string
	doc  Document
}

// Open reads a file or starts from the bundled document.
func Open(path string) (*Store, error) {
	s := &Store{path: path, doc: Bundled()}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	doc, err := Parse(raw)
	if err != nil {
		return s, nil
	}
	s.doc = doc
	return s, nil
}

// Current returns the active document.
func (s *Store) Current() Document { return s.doc }

// Apply verifies, parses, and atomically replaces the active document.
// A bad update leaves the previous document in place.
func (s *Store) Apply(pub ed25519.PublicKey, raw, sig []byte) error {
	if err := Verify(pub, raw, sig); err != nil {
		return err
	}
	doc, err := Parse(raw)
	if err != nil {
		return err
	}
	if s.path != "" {
		// No lock: compat data is written only by the server, which already
		// serialises it; the unique temp name per write is what keeps two
		// writers from corrupting the file.
		if err := fslock.WriteAtomic(s.path, raw, 0o600); err != nil {
			return err
		}
	}
	s.doc = doc
	return nil
}

// MustJSON renders a document for tests.
func MustJSON(doc Document) []byte {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}
