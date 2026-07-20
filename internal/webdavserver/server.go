package webdavserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/xml"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/observability"
	"inkflow/internal/state"
)

const shutdownTimeout = 10 * time.Second
const defaultMaxUploadBytes int64 = 100 * 1024 * 1024

type Server struct {
	cfg     *config.Config
	imp     *importer.Importer
	store   *state.Store
	metrics *observability.Metrics
	logger  *slog.Logger
}

func Serve(ctx context.Context, cfg *config.Config, imp *importer.Importer, store *state.Store, metrics *observability.Metrics, logger *slog.Logger) error {
	if cfg.WebDAVUser == "" {
		cfg.WebDAVUser = os.Getenv("WEBDAV_USER")
	}
	if cfg.WebDAVPass == "" {
		cfg.WebDAVPass = os.Getenv("WEBDAV_PASS")
	}
	srv := &Server{cfg: cfg, imp: imp, store: store, metrics: metrics, logger: logger}
	if cfg.WebDAVUser == "" && cfg.WebDAVPass == "" && !isLoopbackListenAddr(cfg.ListenAddr) {
		srv.warn("UNSAFE WEBDAV CONFIGURATION: unauthenticated plaintext vault writes are reachable on a non-loopback address; configure credentials and use TLS via a reverse proxy", "listen_addr", cfg.ListenAddr)
	}
	httpSrv := newHTTPServer(cfg, srv)

	go func() {
		<-ctx.Done()
		srv.shutdown(httpSrv)
	}()

	srv.info("webdav server starting", "listen_addr", cfg.ListenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		s.handleHealth(w)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/metrics" && s.cfg.Observability.MetricsEnabled && s.cfg.Observability.MetricsAddr == "" {
		if !s.authorize(w, r) {
			return
		}
		s.metrics.Handler().ServeHTTP(w, r)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	defer r.Body.Close()

	clean := cleanPath(r.URL.Path)
	s.info("webdav request", "method", r.Method, "path", clean, "depth", r.Header.Get("Depth"))
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, PUT")
		w.Header().Set("DAV", "1,2")
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		s.handlePropfind(w, r)
	case http.MethodPut:
		s.handlePut(w, r, clean)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.WebDAVUser == "" && s.cfg.WebDAVPass == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	configuredUser := sha256.Sum256([]byte(s.cfg.WebDAVUser))
	suppliedUser := sha256.Sum256([]byte(user))
	configuredPass := sha256.Sum256([]byte(s.cfg.WebDAVPass))
	suppliedPass := sha256.Sum256([]byte(pass))
	if ok && subtle.ConstantTimeCompare(suppliedUser[:], configuredUser[:]) == 1 && subtle.ConstantTimeCompare(suppliedPass[:], configuredPass[:]) == 1 {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="inkflow"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, clean string) {
	route := s.routeLabel(clean)
	if clean == "" {
		s.metrics.Import(route, "rejected")
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	maxUploadBytes := s.cfg.MaxUploadBytes
	if maxUploadBytes == 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	if r.ContentLength > maxUploadBytes {
		s.metrics.Import(route, "rejected")
		http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	started := time.Now().UTC()
	rec, err := s.imp.Import(r.Context(), clean, body, started)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.metrics.Import(route, "rejected")
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		s.error("webdav import failed", "path", clean, "err", err)
		s.metrics.Import(route, "failed")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rec.ImportedAt.Before(started) {
		s.metrics.Import(route, "deduplicated")
		s.metrics.DedupSkip()
	} else {
		s.metrics.Import(route, "succeeded")
	}
	s.info("webdav imported", "path", clean, "note", rec.VaultNotePath, "pdf", rec.VaultPDFPath)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleHealth(w http.ResponseWriter) {
	if s.store == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	pending, err := s.store.GetPendingAndFailedAIImports()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.metrics != nil {
		s.metrics.QueueDepth(len(pending))
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) routeLabel(clean string) string {
	best := ""
	for _, route := range s.cfg.Routes {
		prefix := config.NormalizeRoutePrefix(route.From)
		if strings.HasPrefix(clean+"/", strings.TrimPrefix(prefix, "/")) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return "unmatched"
	}
	return best
}

func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request) {
	clean, target, info, err := s.resolveVaultPath(r.URL.EscapedPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	depth := r.Header.Get("Depth")
	if depth != "" && depth != "0" && depth != "1" {
		http.Error(w, "unsupported Depth", http.StatusBadRequest)
		return
	}
	responses := []propResponse{s.responseFor(clean, info)}
	if depth == "1" && info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			http.Error(w, "unable to list resource", http.StatusInternalServerError)
			return
		}
		for _, entry := range entries {
			childClean := path.Join(clean, entry.Name())
			_, _, childInfo, err := s.resolveVaultPath("/" + childClean)
			if err != nil {
				// Do not follow a symlink (including one that escapes the vault).
				continue
			}
			responses = append(responses, s.responseFor(childClean, childInfo))
		}
	}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_ = xml.NewEncoder(w).Encode(multistatus{XMLName: xml.Name{Space: "DAV:", Local: "multistatus"}, XMLNSD: "DAV:", Responses: responses})
}

func (s *Server) responseFor(clean string, info os.FileInfo) propResponse {
	href := "/" + strings.TrimPrefix(clean, "/")
	href = escapeHref(href)
	prop := prop{
		ResourceType: resourceType{},
		LastModified: info.ModTime().UTC().Format(http.TimeFormat),
	}
	if clean == "" {
		prop.Displayname = info.Name()
	} else {
		prop.Displayname = path.Base(clean)
	}
	if info.IsDir() {
		href = strings.TrimSuffix(href, "/") + "/"
		prop.ResourceType.Collection = &struct{}{}
		prop.ContentType = "httpd/unix-directory"
	} else {
		size := info.Size()
		prop.ContentLength = &size
		prop.ContentType = "application/octet-stream"
	}
	return propResponse{Href: href, Propstat: propstat{Prop: prop, Status: "HTTP/1.1 200 OK"}}
}

// resolveVaultPath decodes and validates a request target before it ever
// reaches the filesystem. Existing symlinks are deliberately rejected: DAV
// browsing must not expose a target outside the configured vault.
func (s *Server) resolveVaultPath(rawPath string) (string, string, os.FileInfo, error) {
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", "", nil, err
	}
	if strings.Contains(decoded, "\\") {
		return "", "", nil, errors.New("backslash path separator")
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == ".." {
			return "", "", nil, errors.New("path traversal")
		}
	}
	clean := cleanPath(decoded)
	root, err := filepath.Abs(s.cfg.VaultDir)
	if err != nil {
		return "", "", nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", nil, err
	}
	target := root
	for _, component := range strings.Split(clean, "/") {
		if component == "" {
			continue
		}
		target = filepath.Join(target, component)
		info, err := os.Lstat(target)
		if err != nil {
			return "", "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", nil, errors.New("symlink target")
		}
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", nil, errors.New("path outside vault")
	}
	lstat, err := os.Lstat(target)
	if err != nil {
		return "", "", nil, err
	}
	if lstat.Mode()&os.ModeSymlink != 0 {
		return "", "", nil, errors.New("symlink target")
	}
	return clean, target, lstat, nil
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func escapeHref(href string) string {
	parts := strings.Split(strings.TrimPrefix(href, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/" + strings.Join(parts, "/")
}

func (s *Server) info(msg string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Info(msg, args...)
	}
}

func (s *Server) error(msg string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Error(msg, args...)
	}
}

func (s *Server) warn(msg string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Warn(msg, args...)
	}
}

func (s *Server) shutdown(httpSrv *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		s.error("webdav server shutdown failed", "err", err)
	}
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeoutDuration,
		ReadTimeout:       cfg.ReadTimeoutDuration,
		WriteTimeout:      cfg.WriteTimeoutDuration,
		IdleTimeout:       cfg.IdleTimeoutDuration,
	}
}

type multistatus struct {
	XMLName   xml.Name       `xml:"D:multistatus"`
	XMLNSD    string         `xml:"xmlns:D,attr"`
	Responses []propResponse `xml:"D:response"`
}

type propResponse struct {
	Href     string   `xml:"D:href"`
	Propstat propstat `xml:"D:propstat"`
}

type propstat struct {
	Prop   prop   `xml:"D:prop"`
	Status string `xml:"D:status"`
}

type prop struct {
	ResourceType  resourceType `xml:"D:resourcetype"`
	Displayname   string       `xml:"D:displayname,omitempty"`
	LastModified  string       `xml:"D:getlastmodified,omitempty"`
	ContentLength *int64       `xml:"D:getcontentlength,omitempty"`
	ContentType   string       `xml:"D:getcontenttype,omitempty"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}
