package gitops

import (
	"errors"
	"sync"
)

// ErrNotWriter is returned when this node must not change GitOps-managed state
// (an HA standby, a node without quorum, a node catching up after a fence).
var ErrNotWriter = errors.New("this node is not the GitOps writer")

// WriterCheck reports whether this node may write GitOps-managed state: commit
// GUI changes to Git, run drift checks against the live system, and apply plans.
// reason explains a false result for logs and API responses.
type WriterCheck func() (ok bool, reason string)

var (
	writerMu    sync.RWMutex
	writerCheck WriterCheck
)

// SetWriterCheck installs the check. Without one (standalone, non-HA) the node
// is always the writer.
func SetWriterCheck(fn WriterCheck) {
	writerMu.Lock()
	writerCheck = fn
	writerMu.Unlock()
}

// IsWriter reports whether this node may write GitOps-managed state.
//
// In an HA pair only the active node with quorum is the writer. A standby has
// no pools imported (shared SAS) or only replication targets (replicated), so
// its live state is not the cluster's state: committing it would push a
// state.yaml without pools, and applying on it would act on storage it does
// not own.
func IsWriter() (bool, string) {
	writerMu.RLock()
	fn := writerCheck
	writerMu.RUnlock()
	if fn == nil {
		return true, ""
	}
	return fn()
}
