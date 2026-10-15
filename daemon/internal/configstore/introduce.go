package configstore

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// Members of a cluster must all be paired with each other: a storage group's
// owner pushes to and pulls from every candidate directly, and configuration
// must keep flowing when any one member is down. Pairing is pairwise (a join
// code per pair), so when a node is added to a cluster, the node adding it
// introduces it to every other member: it asks the member for a join code
// over their paired channel and tells the newcomer to join with it, pinning
// the member's certificate as this node knows it.

// IntroduceRequest tells a paired node to pair with another of this node's peers.
type IntroduceRequest struct {
	NodeID      string `json:"node_id"` // the node to pair with
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	SelfURL     string `json:"self_url"` // the newcomer's address, as this node reaches it
}

type peerAnswer struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Token   string `json:"token"`
}

// Introduce pairs this node's peers newcomer and member with each other.
func Introduce(db *sql.DB, newcomer, member string) error {
	if newcomer == member {
		return nil
	}
	ps, err := loadPeers(db, "")
	if err != nil {
		return err
	}
	var n, m *Peer
	for i := range ps {
		switch ps[i].ID {
		case newcomer:
			n = &ps[i]
		case member:
			m = &ps[i]
		}
	}
	if n == nil || m == nil {
		return ErrUnknownPeer
	}
	var tok peerAnswer
	if err := CallPeer(db, m.ID, "POST", "/api/config/sync/peer/introduce-token", map[string]any{}, &tok); err != nil {
		return fmt.Errorf("asking %s for a join code: %w", m.Name, err)
	}
	if !tok.Success || tok.Token == "" {
		return fmt.Errorf("%s did not issue a join code: %s", m.Name, tok.Error)
	}
	var res peerAnswer
	req := IntroduceRequest{NodeID: m.ID, URL: m.URL, Token: tok.Token, Fingerprint: m.Fingerprint, SelfURL: n.URL}
	if err := CallPeer(db, n.ID, "POST", "/api/config/sync/peer/introduce", req, &res); err != nil {
		return fmt.Errorf("introducing %s to %s: %w", n.Name, m.Name, err)
	}
	if !res.Success {
		return fmt.Errorf("%s could not pair with %s: %s", n.Name, m.Name, res.Error)
	}
	log.Printf("CONFIG SYNC: introduced %s to %s", n.Name, m.Name)
	return nil
}

// AcceptIntroduction pairs this node with the node a peer introduced. A node
// already paired with it is left as it is (pairing again would start the
// exchange over).
func (s *Syncer) AcceptIntroduction(req IntroduceRequest) error {
	if req.NodeID == "" || req.URL == "" || req.Token == "" || req.SelfURL == "" {
		return errors.New("incomplete introduction")
	}
	existing, err := loadPeers(s.db, req.NodeID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	p, err := s.join(req.URL, req.Token, req.SelfURL, req.Fingerprint)
	if err != nil {
		return err
	}
	if p.ID != req.NodeID {
		_ = s.RemovePeer(p.ID)
		return fmt.Errorf("the node at %s is not the one introduced", req.URL)
	}
	return nil
}
