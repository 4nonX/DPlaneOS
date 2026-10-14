package configstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Sealed documents between paired nodes: settings that carry secrets (the
// Git backend's settings with its credentials) travel encrypted with the
// pair's transport key, like secret material (section 5.9).

// SealedDoc is a document on the wire.
type SealedDoc struct {
	Name     string          `json:"name"`
	Material *sealedMaterial `json:"material"`
}

// SendSealed sends doc (JSON) to every paired node at path. Unreachable
// nodes are returned as errors; the caller retries later.
func SendSealed(db *sql.DB, path, name string, doc any) []error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return []error{err}
	}
	peers, err := loadPeers(db, "")
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, p := range peers {
		sealed, err := sealForPeer(p.secret, "doc", name, Material{"doc": string(raw)})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := CallPeer(db, p.ID, "POST", path, SealedDoc{Name: name, Material: sealed}, nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errs
}

// OpenSealed decrypts a document a paired node sent (the request already
// authenticated as peer p) into out.
func OpenSealed(p *Peer, name string, in SealedDoc, out any) error {
	if in.Material == nil || in.Name != name {
		return errors.New("unexpected document")
	}
	m, err := openFromPeer(p.secret, "doc", name, in.Material)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(m["doc"]), out)
}
