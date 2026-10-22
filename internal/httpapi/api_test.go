package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pequin/go-shorturl/internal/links"
)

var fixedNow = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

func newTestAPI(t *testing.T, store Store, codes ...string) *API {
	t.Helper()
	base, err := url.Parse("https://sho.rt")
	if err != nil {
		t.Fatal(err)
	}
	a := New(store, base, slog.New(slog.DiscardHandler))
	a.now = func() time.Time { return fixedNow }
	a.newCode = func() string {
		if len(codes) == 0 {
			t.Fatal("test ran out of codes")
		}
		c := codes[0]
		codes = codes[1:]
		return c
	}
	return a
}

// seededStore returns a store that already has "taken" -> https://example.com/old.
func seededStore(t *testing.T) *links.Memory {
	t.Helper()
	m := links.NewMemory()
	if err := m.Create(t.Context(), links.Link{Code: "taken", URL: "https://example.com/old", CreatedAt: fixedNow}); err != nil {
		t.Fatal(err)
	}
	return m
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

func TestCreateLink(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		codes       []string
		wantStatus  int
		wantCode    string // for 201
		wantErr     string // substring of the error message, for non-2xx
	}{
		{name: "generated code", body: `{"url":"https://example.com/a"}`, codes: []string{"abc2345"}, wantStatus: 201, wantCode: "abc2345"},
		{name: "retries on collision", body: `{"url":"https://example.com/a"}`, codes: []string{"taken", "fresh77"}, wantStatus: 201, wantCode: "fresh77"},
		{name: "custom alias", body: `{"url":"http://example.com","alias":"My_Link-1"}`, wantStatus: 201, wantCode: "My_Link-1"},
		{name: "charset in content type", contentType: "application/json; charset=utf-8", body: `{"url":"https://example.com"}`, codes: []string{"abc2345"}, wantStatus: 201, wantCode: "abc2345"},

		{name: "alias taken", body: `{"url":"https://example.com","alias":"taken"}`, wantStatus: 409, wantErr: "already taken"},
		{name: "alias too short", body: `{"url":"https://example.com","alias":"ab"}`, wantStatus: 400, wantErr: "3-32 characters"},
		{name: "alias with slash", body: `{"url":"https://example.com","alias":"a/b/c"}`, wantStatus: 400, wantErr: "3-32 characters"},
		{name: "reserved alias any case", body: `{"url":"https://example.com","alias":"HealthZ"}`, wantStatus: 400, wantErr: "reserved"},
		{name: "missing url", body: `{}`, wantStatus: 400, wantErr: "url is required"},
		{name: "ftp scheme", body: `{"url":"ftp://example.com/file"}`, wantStatus: 400, wantErr: "http or https"},
		{name: "relative url", body: `{"url":"/just/a/path"}`, wantStatus: 400, wantErr: "http or https"},
		{name: "no host", body: `{"url":"https:///path"}`, wantStatus: 400, wantErr: "include a host"},
		{name: "unparsable url", body: `{"url":"https://exa mple.com:port"}`, wantStatus: 400, wantErr: "not valid"},
		{name: "own host", body: `{"url":"https://SHO.RT/abc"}`, wantStatus: 400, wantErr: "this shortener"},
		{name: "url too long", body: `{"url":"https://example.com/` + strings.Repeat("a", maxURLLength) + `"}`, wantStatus: 400, wantErr: "longer than"},
		{name: "malformed json", body: `{"url":`, wantStatus: 400, wantErr: "invalid JSON"},
		{name: "unknown field", body: `{"url":"https://example.com","ttl":5}`, wantStatus: 400, wantErr: "unknown field"},
		{name: "two objects", body: `{"url":"https://example.com"}{"url":"https://example.org"}`, wantStatus: 400, wantErr: "trailing data"},
		{name: "wrong content type", contentType: "text/plain", body: `{"url":"https://example.com"}`, wantStatus: 415, wantErr: "application/json"},
		{name: "body too large", body: `{"url":"https://example.com","alias":"` + strings.Repeat("a", maxBodyBytes) + `"}`, wantStatus: 413, wantErr: "at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAPI(t, seededStore(t), tt.codes...)
			req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", cmp.Or(tt.contentType, "application/json"))
			rec := httptest.NewRecorder()

			a.Handler().ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("POST /api/links %s: status = %d, want %d; body: %s", tt.body, rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus != http.StatusCreated {
				if msg := decodeError(t, rec); !strings.Contains(msg, tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", msg, tt.wantErr)
				}
				return
			}
			var got linkResponse
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Code != tt.wantCode || got.ShortURL != "https://sho.rt/"+tt.wantCode || !got.CreatedAt.Equal(fixedNow) {
				t.Errorf("response = %+v, want code %q at %s", got, tt.wantCode, fixedNow)
			}
			if loc := rec.Header().Get("Location"); loc != "/api/links/"+tt.wantCode {
				t.Errorf("Location = %q", loc)
			}
		})
	}
}

func TestCreateLinkGivesUpAfterRepeatedCollisions(t *testing.T) {
	codes := make([]string, maxCodeAttempts)
	for i := range codes {
		codes[i] = "taken"
	}
	a := newTestAPI(t, seededStore(t), codes...)
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"https://example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	a.Handler().ServeHTTP(rec, req)

	// Exhausting the code space is our problem, not a client conflict.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRedirectAndShow(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantStatus   int
		wantLocation string
		wantVisits   int64
	}{
		{name: "redirect known code", path: "/taken", wantStatus: http.StatusFound, wantLocation: "https://example.com/old"},
		{name: "redirect unknown code", path: "/missing", wantStatus: http.StatusNotFound},
		{name: "show known code", path: "/api/links/taken", wantStatus: http.StatusOK, wantVisits: 0},
		{name: "show unknown code", path: "/api/links/missing", wantStatus: http.StatusNotFound},
		{name: "health", path: "/healthz", wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestAPI(t, seededStore(t))
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("GET %s: status = %d, want %d", tt.path, rec.Code, tt.wantStatus)
			}
			switch rec.Code {
			case http.StatusFound:
				if loc := rec.Header().Get("Location"); loc != tt.wantLocation {
					t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
				}
			case http.StatusNotFound:
				if msg := decodeError(t, rec); msg != "link not found" {
					t.Errorf("error = %q", msg)
				}
			case http.StatusOK:
				var got linkResponse
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.Visits != tt.wantVisits || got.URL != "https://example.com/old" {
					t.Errorf("GET %s = %+v", tt.path, got)
				}
			}
		})
	}
}

type brokenStore struct{ err error }

func (s brokenStore) Create(context.Context, links.Link) error { return s.err }
func (s brokenStore) Resolve(context.Context, string) (links.Link, error) {
	return links.Link{}, s.err
}
func (s brokenStore) Link(context.Context, string) (links.Link, error) { return links.Link{}, s.err }

func TestStoreFailureIsHiddenFromClient(t *testing.T) {
	store := brokenStore{err: errors.New("disk on fire at /var/lib/shorturl")}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/abc", nil),
		httptest.NewRequest(http.MethodGet, "/api/links/abc", nil),
		httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{"url":"https://example.com","alias":"abc"}`)),
	}
	for _, req := range requests {
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newTestAPI(t, store).Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s: status = %d, want 500", req.Method, req.URL.Path, rec.Code)
			continue
		}
		if msg := decodeError(t, rec); msg != "internal error" {
			t.Errorf("%s %s: error = %q leaks internals", req.Method, req.URL.Path, msg)
		}
	}
}

func TestRandomCode(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		c := randomCode()
		if len(c) != codeLength || strings.Trim(c, codeAlphabet) != "" {
			t.Fatalf("randomCode() = %q, want %d chars from %q", c, codeLength, codeAlphabet)
		}
		seen[c] = true
	}
	// 32^7 ≈ 3.4e10 possible codes; a duplicate in 1000 draws means the
	// generator is broken, not unlucky.
	if len(seen) != 1000 {
		t.Errorf("got %d distinct codes out of 1000", len(seen))
	}
}
