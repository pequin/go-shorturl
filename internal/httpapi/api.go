// Package httpapi serves the shortener's JSON API and redirects.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/pequin/go-shorturl/internal/links"
)

const (
	maxBodyBytes    = 4 << 10
	maxURLLength    = 2048
	codeLength      = 7
	maxCodeAttempts = 5
)

// 32 symbols without the look-alikes 0/o and 1/l. Because 256 is a multiple
// of 32, mapping a random byte with b%32 has no modulo bias.
const codeAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

var (
	aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)
	// Aliases that would shadow fixed routes or be confusing next to them.
	reservedAliases = map[string]bool{"api": true, "healthz": true}
)

// Store is the storage the API depends on. links.Memory implements it;
// tests substitute a failing store to cover the 500 paths.
type Store interface {
	Create(ctx context.Context, l links.Link) error
	Resolve(ctx context.Context, code string) (links.Link, error)
	Link(ctx context.Context, code string) (links.Link, error)
}

// API holds the handler dependencies. Create it with New.
type API struct {
	store   Store
	baseURL *url.URL
	logger  *slog.Logger
	newCode func() string
	now     func() time.Time
}

// New returns an API that builds short URLs under baseURL.
func New(store Store, baseURL *url.URL, logger *slog.Logger) *API {
	return &API{
		store:   store,
		baseURL: baseURL,
		logger:  logger,
		newCode: randomCode,
		now:     time.Now,
	}
}

// Handler returns the routes wrapped in request logging.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/links", a.createLink)
	mux.HandleFunc("GET /api/links/{code}", a.showLink)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /{code}", a.redirect)
	return a.logRequests(mux)
}

type createRequest struct {
	URL   string `json:"url"`
	Alias string `json:"alias"`
}

type linkResponse struct {
	Code      string    `json:"code"`
	ShortURL  string    `json:"short_url"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Visits    int64     `json:"visits"`
}

func (a *API) createLink(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.validateTarget(req.URL); err != nil {
		a.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	link := links.Link{URL: req.URL, CreatedAt: a.now().UTC()}
	var err error
	if req.Alias != "" {
		if err = validateAlias(req.Alias); err != nil {
			a.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		link.Code = req.Alias
		err = a.store.Create(r.Context(), link)
	} else {
		link, err = a.createWithRandomCode(r.Context(), link)
	}

	switch {
	case errors.Is(err, links.ErrCodeTaken):
		a.writeError(w, r, http.StatusConflict, "alias is already taken")
		return
	case err != nil:
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/links/"+link.Code)
	a.writeJSON(w, r, http.StatusCreated, a.toResponse(link))
}

// createWithRandomCode never returns links.ErrCodeTaken, so the caller can
// treat that error as an alias conflict.
func (a *API) createWithRandomCode(ctx context.Context, link links.Link) (links.Link, error) {
	for range maxCodeAttempts {
		link.Code = a.newCode()
		err := a.store.Create(ctx, link)
		if !errors.Is(err, links.ErrCodeTaken) {
			return link, err
		}
	}
	return links.Link{}, fmt.Errorf("no free code after %d attempts", maxCodeAttempts)
}

func (a *API) redirect(w http.ResponseWriter, r *http.Request) {
	link, err := a.store.Resolve(r.Context(), r.PathValue("code"))
	if a.handleLookupError(w, r, err) {
		return
	}
	// 302 rather than 301: browsers cache permanent redirects and the visit
	// counter would stop seeing repeat clicks.
	http.Redirect(w, r, link.URL, http.StatusFound)
}

func (a *API) showLink(w http.ResponseWriter, r *http.Request) {
	link, err := a.store.Link(r.Context(), r.PathValue("code"))
	if a.handleLookupError(w, r, err) {
		return
	}
	a.writeJSON(w, r, http.StatusOK, a.toResponse(link))
}

func (a *API) handleLookupError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, links.ErrNotFound):
		a.writeError(w, r, http.StatusNotFound, "link not found")
	default:
		a.internalError(w, r, err)
	}
	return true
}

func (a *API) validateTarget(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > maxURLLength {
		return fmt.Errorf("url is longer than %d bytes", maxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	if u.Host == "" {
		return errors.New("url must include a host")
	}
	// Short links to our own short links can be chained into redirect loops.
	if strings.EqualFold(u.Host, a.baseURL.Host) {
		return errors.New("url points to this shortener")
	}
	return nil
}

func validateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) {
		return errors.New("alias must be 3-32 characters: letters, digits, '-' or '_'")
	}
	if reservedAliases[strings.ToLower(alias)] {
		return errors.New("alias is reserved")
	}
	return nil
}

func (a *API) toResponse(l links.Link) linkResponse {
	return linkResponse{
		Code:      l.Code,
		ShortURL:  a.baseURL.JoinPath(l.Code).String(),
		URL:       l.URL,
		CreatedAt: l.CreatedAt,
		Visits:    l.Visits,
	}
}

func randomCode() string {
	b := make([]byte, codeLength)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24; it crashes the program instead.
	for i := range b {
		b[i] = codeAlphabet[b[i]%byte(len(codeAlphabet))]
	}
	return string(b)
}

// decodeJSON writes the error response itself and reports whether the
// handler should continue.
func (a *API) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		a.writeError(w, r, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil && dec.More() {
		err = errors.New("trailing data after JSON object")
	}
	if err == nil {
		return true
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		a.writeError(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("body must be at most %d bytes", maxBodyBytes))
		return false
	}
	a.writeError(w, r, http.StatusBadRequest, "invalid JSON body: "+err.Error())
	return false
}
