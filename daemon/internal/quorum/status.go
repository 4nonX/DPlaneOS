package quorum

import (
	"bufio"
	"strconv"
	"strings"
)

// Member is one line of the membership table.
type Member struct {
	NodeID  int    `json:"nodeid"`
	Votes   int    `json:"votes"`
	Qdevice string `json:"qdevice,omitempty"` // e.g. "A,V,NMW" (alive, voting, not-master-wins)
	Name    string `json:"name"`
	Local   bool   `json:"local"`
}

// Status is the parsed output of `corosync-quorumtool -s`.
type Status struct {
	Running       bool     `json:"running"`
	Quorate       bool     `json:"quorate"`
	Nodes         int      `json:"nodes"`
	LocalNodeID   int      `json:"local_nodeid"`
	ExpectedVotes int      `json:"expected_votes"`
	TotalVotes    int      `json:"total_votes"`
	QuorumVotes   int      `json:"quorum_votes"`
	Flags         []string `json:"flags"`
	Members       []Member `json:"members"`
	QDeviceVotes  int      `json:"qdevice_votes"` // votes the third vote currently contributes
	QDeviceAlive  bool     `json:"qdevice_alive"` // flag "Qdevice" present and the device votes
}

// HasFlag reports whether votequorum reported flag f (e.g. "2Node", "Qdevice").
func (s Status) HasFlag(f string) bool {
	for _, x := range s.Flags {
		if x == f {
			return true
		}
	}
	return false
}

// ParseQuorumtool parses `corosync-quorumtool -s` output. Corosync prints
// the same layout with and without a quorum device; the membership table
// gains a "Qdevice" column and a row for the device when one is configured.
func ParseQuorumtool(out string) Status {
	st := Status{Running: strings.Contains(out, "Quorum information")}
	sc := bufio.NewScanner(strings.NewReader(out))
	inMembers := false
	hasQdeviceCol := false
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "Nodeid") && strings.Contains(trim, "Votes") {
			inMembers = true
			hasQdeviceCol = strings.Contains(trim, "Qdevice")
			continue
		}
		if inMembers {
			if m, ok := parseMember(trim, hasQdeviceCol); ok {
				if m.NodeID == 0 && strings.EqualFold(m.Name, "Qdevice") {
					st.QDeviceVotes = m.Votes
					continue
				}
				st.Members = append(st.Members, m)
			}
			continue
		}
		key, val, ok := strings.Cut(trim, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "Quorate":
			st.Quorate = strings.EqualFold(val, "Yes")
		case "Nodes":
			st.Nodes = atoi(val)
		case "Node ID":
			st.LocalNodeID = atoiPrefix(val)
		case "Expected votes":
			st.ExpectedVotes = atoi(val)
		case "Total votes":
			st.TotalVotes = atoi(val)
		case "Quorum":
			st.QuorumVotes = atoiPrefix(val)
		case "Flags":
			st.Flags = strings.Fields(val)
		}
	}
	st.QDeviceAlive = st.HasFlag("Qdevice") && st.QDeviceVotes > 0
	return st
}

func parseMember(line string, qdeviceCol bool) (Member, bool) {
	f := strings.Fields(line)
	if len(f) < 3 {
		return Member{}, false
	}
	id, err := strconv.Atoi(f[0])
	if err != nil {
		return Member{}, false
	}
	m := Member{NodeID: id, Votes: atoi(f[1])}
	rest := f[2:]
	// The Qdevice column holds flags such as "A,V,NMW" (or "NR" when the node
	// is not registered); the device row has no value in it.
	if qdeviceCol && len(rest) > 1 && isQdeviceFlags(rest[0]) {
		m.Qdevice, rest = rest[0], rest[1:]
	}
	if len(rest) > 0 && rest[len(rest)-1] == "(local)" {
		m.Local = true
		rest = rest[:len(rest)-1]
	}
	m.Name = strings.Join(rest, " ")
	return m, true
}

func isQdeviceFlags(s string) bool {
	for _, p := range strings.Split(s, ",") {
		switch p {
		case "A", "V", "NV", "NA", "MW", "NMW", "NR":
		default:
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// atoiPrefix parses a leading integer ("2  Activity blocked" → 2).
func atoiPrefix(s string) int {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	return atoi(f[0])
}
