package gitsync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI records requests and answers from a routing function.
type fakeAPI struct {
	srv  *httptest.Server
	seen []string // "METHOD path?query auth"
	body []map[string]any
}

func newFakeAPI(t *testing.T, route func(method, path string, body map[string]any) (int, any)) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		auth := r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")
		f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI()+" "+auth)
		f.body = append(f.body, body)
		code, out := route(r.Method, r.URL.EscapedPath(), body)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestGitHubProvider(t *testing.T) {
	created := false
	f := newFakeAPI(t, func(m, p string, b map[string]any) (int, any) {
		switch {
		case m == "GET" && p == "/repos/acme/nas/pulls":
			if created {
				return 200, []any{map[string]any{"number": 7, "html_url": "https://gh/pr/7", "state": "open"}}
			}
			return 200, []any{}
		case m == "POST" && p == "/repos/acme/nas/pulls":
			created = true
			return 201, map[string]any{"number": 7, "html_url": "https://gh/pr/7", "state": "open"}
		case m == "GET" && p == "/repos/acme/nas/pulls/7":
			return 200, map[string]any{"number": 7, "html_url": "https://gh/pr/7", "state": "closed", "merged": true}
		case m == "POST" && strings.HasPrefix(p, "/repos/acme/nas/statuses/"):
			return 201, map[string]any{}
		}
		return 404, map[string]any{"message": "not found"}
	})
	prov, err := NewProvider(ProviderConfig{Kind: ProviderGitHub, APIURL: f.srv.URL, Repo: "acme/nas", Token: "ghp_x"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := prov.EnsurePR("dplaneos/cluster", "main", "Cluster changes", "body")
	if err != nil || pr.Number != 7 || pr.State != PROpen {
		t.Fatalf("open: %+v %v", pr, err)
	}
	if !strings.Contains(f.seen[0], "head=acme%3Adplaneos%2Fcluster") || !strings.Contains(f.seen[0], "Bearer ghp_x") {
		t.Errorf("lookup request: %s", f.seen[0])
	}
	if f.body[1]["head"] != "dplaneos/cluster" || f.body[1]["base"] != "main" {
		t.Errorf("create body: %v", f.body[1])
	}
	// Second time: found, not created again.
	if _, err := prov.EnsurePR("dplaneos/cluster", "main", "x", "y"); err != nil || len(f.seen) != 3 {
		t.Fatalf("existing PR: %v %v", err, f.seen)
	}
	if pr, _ := prov.GetPR(7); pr.State != PRMerged {
		t.Errorf("merged: %+v", pr)
	}
	if err := prov.SetStatus("abc", StatusFailure, "apply failed"); err != nil || f.body[len(f.body)-1]["state"] != "failure" || f.body[len(f.body)-1]["context"] != "dplaneos" {
		t.Errorf("status: %v %v", err, f.body[len(f.body)-1])
	}
}

func TestGiteaProvider(t *testing.T) {
	f := newFakeAPI(t, func(m, p string, b map[string]any) (int, any) {
		switch {
		case m == "GET" && p == "/api/v1/repos/acme/nas/pulls":
			return 200, []any{
				map[string]any{"number": 3, "html_url": "u3", "state": "open", "head": map[string]any{"ref": "other"}, "base": map[string]any{"ref": "main"}},
				map[string]any{"number": 4, "html_url": "u4", "state": "open", "head": map[string]any{"ref": "dplaneos/groups/data"}, "base": map[string]any{"ref": "main"}},
			}
		case m == "GET" && p == "/api/v1/repos/acme/nas/pulls/4":
			return 200, map[string]any{"number": 4, "state": "closed", "merged": false}
		case m == "POST" && strings.HasPrefix(p, "/api/v1/repos/acme/nas/statuses/"):
			return 201, map[string]any{}
		}
		return 404, map[string]any{}
	})
	prov, _ := NewProvider(ProviderConfig{Kind: ProviderGitea, APIURL: f.srv.URL + "/api/v1", Repo: "acme/nas", Token: "t0k"})
	pr, err := prov.EnsurePR("dplaneos/groups/data", "main", "t", "b")
	if err != nil || pr.Number != 4 || !strings.HasSuffix(f.seen[0], "token t0k") {
		t.Fatalf("find: %+v %v %v", pr, err, f.seen)
	}
	if pr, _ := prov.GetPR(4); pr.State != PRClosed {
		t.Errorf("closed: %+v", pr)
	}
	if err := prov.SetStatus("abc", StatusSuccess, "applied"); err != nil || f.body[len(f.body)-1]["state"] != "success" {
		t.Errorf("status: %v", err)
	}
}

func TestGitLabProvider(t *testing.T) {
	f := newFakeAPI(t, func(m, p string, b map[string]any) (int, any) {
		switch {
		case m == "GET" && p == "/api/v4/projects/infra%2Fstorage%2Fnas/merge_requests":
			return 200, []any{}
		case m == "POST" && p == "/api/v4/projects/infra%2Fstorage%2Fnas/merge_requests":
			return 201, map[string]any{"iid": 12, "web_url": "https://gl/mr/12", "state": "opened"}
		case m == "GET" && p == "/api/v4/projects/infra%2Fstorage%2Fnas/merge_requests/12":
			return 200, map[string]any{"iid": 12, "state": "merged"}
		case m == "POST" && strings.HasPrefix(p, "/api/v4/projects/infra%2Fstorage%2Fnas/statuses/"):
			return 201, map[string]any{}
		}
		return 404, map[string]any{"message": p}
	})
	prov, _ := NewProvider(ProviderConfig{Kind: ProviderGitLab, APIURL: f.srv.URL + "/api/v4", Repo: "infra/storage/nas", Token: "glpat"})
	pr, err := prov.EnsurePR("dplaneos/cluster", "main", "t", "b")
	if err != nil || pr.Number != 12 || pr.State != PROpen || !strings.HasSuffix(f.seen[0], "glpat") {
		t.Fatalf("create: %+v %v %v", pr, err, f.seen)
	}
	if f.body[1]["source_branch"] != "dplaneos/cluster" || f.body[1]["target_branch"] != "main" {
		t.Errorf("body: %v", f.body[1])
	}
	if pr, _ := prov.GetPR(12); pr.State != PRMerged {
		t.Errorf("merged: %+v", pr)
	}
	if err := prov.SetStatus("abc", StatusFailure, "x"); err != nil || f.body[len(f.body)-1]["state"] != "failed" {
		t.Errorf("status: %v %v", err, f.body[len(f.body)-1])
	}
}

func TestProviderErrorsAndValidation(t *testing.T) {
	f := newFakeAPI(t, func(m, p string, b map[string]any) (int, any) {
		return 403, map[string]any{"message": "Resource not accessible"}
	})
	prov, _ := NewProvider(ProviderConfig{Kind: ProviderGitHub, APIURL: f.srv.URL, Repo: "a/b", Token: "t"})
	_, err := prov.EnsurePR("h", "main", "t", "b")
	var ae *APIError
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !asAPIError(err, &ae) {
		t.Fatalf("error: %v", err)
	}
	for _, bad := range []ProviderConfig{
		{Kind: ProviderGitHub, Repo: "noslash", Token: "t"},
		{Kind: ProviderGitHub, Repo: "a/b"},
		{Kind: ProviderGitea, Repo: "a/b", Token: "t", APIURL: "ftp://x"},
		{Kind: "bitbucket", Repo: "a/b", Token: "t", APIURL: "https://x"},
	} {
		if _, err := NewProvider(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if p, err := NewProvider(ProviderConfig{Kind: ProviderNone}); p != nil || err != nil {
		t.Error("none")
	}
}

func asAPIError(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}
