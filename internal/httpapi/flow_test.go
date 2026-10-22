package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pequin/go-shorturl/internal/httpapi"
	"github.com/pequin/go-shorturl/internal/links"
)

func startServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	base, err := url.Parse("https://sho.rt")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(links.NewMemory(), base, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(srv.Close)

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return srv, client
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func do(t *testing.T, client *http.Client, method, target, body string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: data}
}

// TestShortenFollowAndCount drives the real store through a real HTTP
// server and client, the way a user would.
func TestShortenFollowAndCount(t *testing.T) {
	srv, client := startServer(t)

	res := do(t, client, http.MethodPost, srv.URL+"/api/links", `{"url":"https://go.dev/doc/"}`)
	var created struct {
		Code     string `json:"code"`
		ShortURL string `json:"short_url"`
	}
	if err := json.Unmarshal(res.body, &created); err != nil || res.status != http.StatusCreated {
		t.Fatalf("create: status %d, body %s, decode err %v", res.status, res.body, err)
	}
	if !strings.HasPrefix(created.ShortURL, "https://sho.rt/") {
		t.Errorf("short_url = %q, want it under BASE_URL", created.ShortURL)
	}

	for range 2 {
		res = do(t, client, http.MethodGet, srv.URL+"/"+created.Code, "")
		if res.status != http.StatusFound || res.header.Get("Location") != "https://go.dev/doc/" {
			t.Fatalf("follow: status %d, Location %q", res.status, res.header.Get("Location"))
		}
	}

	res = do(t, client, http.MethodGet, srv.URL+"/api/links/"+created.Code, "")
	var stats struct {
		Visits int64 `json:"visits"`
	}
	if err := json.Unmarshal(res.body, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Visits != 2 {
		t.Errorf("visits = %d after two redirects, want 2", stats.Visits)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, client := startServer(t)

	res := do(t, client, http.MethodDelete, srv.URL+"/api/links", "")
	if res.status != http.StatusMethodNotAllowed || res.header.Get("Allow") == "" {
		t.Errorf("DELETE /api/links: status %d, Allow %q", res.status, res.header.Get("Allow"))
	}
}
