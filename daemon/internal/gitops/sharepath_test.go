package gitops

import "testing"

// A share or export is on a dataset when its path is the dataset's
// mountpoint or below it, whether or not the file spells the mountpoint out.
func TestSharesOnDatasetsAreNotBlocked(t *testing.T) {
	live := &LiveState{
		Pools:    []LivePool{{Name: "tank", Health: "ONLINE"}},
		Datasets: []LiveDataset{{Name: "tank/data", Mountpoint: "/tank/data", Compression: "on"}},
		Shares: []LiveShare{
			{Name: "data", Path: "/tank/data", Enabled: true},
			{Name: "public", Path: "/tank/data/pub", ReadOnly: true, GuestOK: true, Enabled: true},
		},
		NFS: []LiveNFSExport{{Path: "/tank/data", Clients: "10.0.2.0/24", Options: "rw", Enabled: true}},
	}
	// The file the system writes about itself.
	desired, err := ParseStateYAML(GenerateStateYAML(live))
	if err != nil {
		t.Fatal(err)
	}
	desired.IgnoreExtraneous = true
	plan := ComputeDiff(desired, live)
	for _, item := range plan.Items {
		if item.Action != ActionNOP {
			t.Errorf("the system's own record plans %s %s/%s: %s %v", item.Action, item.Kind, item.Name, item.BlockReason, item.Changes)
		}
	}

	// New shares: on a declared dataset that does not exist yet, below it,
	// and on a dataset that exists but is not in the file.
	desired.Datasets = append(desired.Datasets, DesiredDataset{Name: "tank/new"})
	desired.Shares = append(desired.Shares,
		DesiredShare{Name: "new", Path: "/tank/new"},
		DesiredShare{Name: "sub", Path: "/tank/new/sub"},
		DesiredShare{Name: "elsewhere", Path: "/srv/files"},
		DesiredShare{Name: "lookalike", Path: "/tank/database"})
	got := map[string]DiffAction{}
	for _, item := range ComputeDiff(desired, live).Items {
		if item.Kind == KindShare {
			got[item.Name] = item.Action
		}
	}
	if got["new"] != ActionCreate || got["sub"] != ActionCreate {
		t.Errorf("shares on a declared dataset must be created: %v", got)
	}
	if got["elsewhere"] != ActionBlocked || got["lookalike"] != ActionBlocked {
		t.Errorf("a share outside every dataset stays blocked (a name that only begins like one, too): %v", got)
	}
}
