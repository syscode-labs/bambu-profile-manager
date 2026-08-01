package webui_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/parser"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage/sqlite"
	"github.com/syscod3/bambu-profile-manager/internal/webui"
)

func loadFixtureSet(t *testing.T) resolver.Set {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "fixtures", "x1c-to-p1s", "source")
	names := []string{
		"leaf.json",
		filepath.Join("system", "Bambu ABS @BBL X1C.json"),
		filepath.Join("system", "Bambu ABS @base.json"),
		filepath.Join("system", "fdm_filament_abs.json"),
		filepath.Join("system", "fdm_filament_common.json"),
	}
	set := resolver.Set{}
	for _, n := range names {
		p, err := parser.Load(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("load %s: %v", n, err)
		}
		set[p.Name] = p
	}
	return set
}

func newTestServer(t *testing.T) (*httptest.Server, *service.Service) {
	t.Helper()
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })

	svc := &service.Service{Repo: repo}
	srv := &webui.Server{Svc: svc}
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts, svc
}

func TestIndexListsNoProfilesInitially(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
}

func TestImportThenListThenDetail(t *testing.T) {
	ts, svc := newTestServer(t)
	set := loadFixtureSet(t)

	var bundleBuf bytes.Buffer
	if _, err := svc.ExportBundle(t.Context(), &bundleBuf, set, "Syscode - AmazonBasics ABS 0.6"); err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "test.profilepack")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(bundleBuf.Bytes()); err != nil {
		t.Fatalf("write part: %v", err)
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/import", &body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /import: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /import status = %d, want 303", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "/profiles/") {
		t.Fatalf("Location = %q, want /profiles/<id>", location)
	}

	detailResp, err := http.Get(ts.URL + location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	defer detailResp.Body.Close()
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", location, detailResp.StatusCode)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(detailResp.Body)
	if !strings.Contains(buf.String(), "Syscode - AmazonBasics ABS 0.6") {
		t.Fatalf("profile detail page missing the profile name: %s", buf.String())
	}

	indexResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer indexResp.Body.Close()
	buf2 := new(bytes.Buffer)
	buf2.ReadFrom(indexResp.Body)
	if !strings.Contains(buf2.String(), "Syscode - AmazonBasics ABS 0.6") {
		t.Fatal("index page does not list the imported profile")
	}
}
