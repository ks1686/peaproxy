// Package compatdata loads versioned capability profiles. Bundled data needs
// no network. Imported updates must be signed and schema-compatible.
package compatdata

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
)

const SchemaVersion = 1

// ErrUnsigned means an update had no valid signature.
var ErrUnsigned = errors.New("compatibility update is unsigned")

// ErrSchema means the document schema cannot be applied.
var ErrSchema = errors.New("incompatible compatibility schema")

// Document is a data-only profile set. It never contains model ids to invent.
type Document struct {
	Schema   int                `json:"schema"`
	Profiles map[string]Profile `json:"profiles"`
}

// Profile is one compatibility fact set.
type Profile struct {
	Tools  string `json:"tools,omitempty"`
	Vision string `json:"vision,omitempty"`
	Cache  string `json:"cache,omitempty"`
	Client string `json:"client,omitempty"`
}

// Parse checks the schema and rejects documents that try to list catalog models.
func Parse(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, err
	}
	if doc.Schema != SchemaVersion {
		return Document{}, ErrSchema
	}
	var extra map[string]json.RawMessage
	if json.Unmarshal(raw, &extra) == nil {
		if _, ok := extra["models"]; ok {
			return Document{}, fmt.Errorf("compatibility data cannot create catalog models")
		}
	}
	if doc.Profiles == nil {
		doc.Profiles = map[string]Profile{}
	}
	return doc, nil
}

// Verify checks an Ed25519 signature over the raw document.
func Verify(pub ed25519.PublicKey, raw, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize || len(sig) == 0 || !ed25519.Verify(pub, raw, sig) {
		return ErrUnsigned
	}
	return nil
}
