package webdavserver

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"inkflow/internal/config"
	"inkflow/internal/importer"
)

type Server struct {
	cfg    *config.Config
	imp    *importer.Importer
	logger *slog.Logger
}

func Serve(ctx context.Context, cfg *config.Config, imp *importer.Importer, logger *slog.Logger) error {
	if cfg.WebDAVUser == "" {
		cfg.WebDAVUser = os.Getenv("WEBDAV_USER")
	}
	if cfg.WebDAVPass == "" {
		cfg.WebDAVPass = os.Getenv("WEBDAV_PASS")
	}
	srv := &Server{cfg: cfg, imp: imp, logger: logger}
	httpSrv := &http.Server{Addr: cfg.ListenAddr, Handler: srv}

	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	srv.info("webdav server starting", "listen_addr", cfg.ListenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clean := cleanPath(r.URL.Path)
	if !s.authorize(w, r) {
		return
	}
	defer r.Body.Close()

	s.info("webdav request", "method", r.Method, "path", clean, "depth", r.Header.Get("Depth"))
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, MKCOL, PUT")
		w.Header().Set("DAV", "1,2")
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		s.handlePropfind(w, r, clean)
	case "MKCOL":
		s.handleMkcol(w, r, clean)
	case http.MethodPut:
		s.handlePut(w, r, clean)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, MKCOL, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	clean := cleanPath(r.URL.Path)
	if s.cfg.WebDAVUser == "" && s.cfg.WebDAVPass == "" {
		s.debug("auth_check", "authenticated", true, "path", clean)
		return true
	}
	user, pass, ok := r.BasicAuth()
	if ok && user == s.cfg.WebDAVUser && pass == s.cfg.WebDAVPass {
		s.debug("auth_check", "authenticated", true, "path", clean)
		return true
	}
	s.debug("auth_check", "authenticated", false, "path", clean)
	s.debug("auth_rejected", "path", clean)
	w.Header().Set("WWW-Authenticate", `Basic realm="inkflow"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, clean string) {
	if clean == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	s.debug("import_dispatch", "path", clean)
	rec, err := s.imp.Import(r.Context(), clean, r.Body, time.Now().UTC())
	if err != nil {
		s.error("webdav import failed", "path", clean, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.info("webdav imported", "path", clean, "note", rec.VaultNotePath, "pdf", rec.VaultPDFPath)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, clean string) {
	responses := []propResponse{s.responseFor(clean)}
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_ = xml.NewEncoder(w).Encode(multistatus{XMLName: xml.Name{Space: "DAV:", Local: "multistatus"}, XMLNSD: "DAV:", Responses: responses})
}

func (s *Server) handleMkcol(w http.ResponseWriter, r *http.Request, clean string) {
	_, target, info, err := s.resolveVaultPathForCreate(r.URL.EscapedPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.debug("mkcol_conflict", "path", clean, "reason", "parent_not_found")
			http.Error(w, "parent collection not found", http.StatusConflict)
			return
		}
		s.debug("mkcol_rejected", "path", clean, "err", err)
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if info != nil {
		if info.IsDir() {
			s.debug("mkcol_already_exists", "path", clean)
			http.Error(w, "collection already exists", http.StatusMethodNotAllowed)
			return
		}
		s.debug("mkcol_conflict", "path", clean, "reason", "target_is_file")
		http.Error(w, "target is not a collection", http.StatusConflict)
		return
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if os.IsExist(err) {
			if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
				s.debug("mkcol_conflict", "path", clean, "reason", "target_is_file")
				http.Error(w, "target is not a collection", http.StatusConflict)
				return
			}
			s.debug("mkcol_already_exists", "path", clean)
			http.Error(w, "collection already exists", http.StatusMethodNotAllowed)
			return
		}
		s.error("webdav mkcol failed", "path", clean, "err", err)
		http.Error(w, "unable to create collection", http.StatusInternalServerError)
		return
	}
	s.debug("mkcol_created", "path", clean)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) responseFor(clean string) propResponse {
	if clean == "" {
		return propResponse{Href: "/", Propstat: propstat{Prop: prop{Displayname: "inkflow", ResourceType: resourceType{Collection: &struct{}{}}, ContentType: "httpd/unix-directory"}, Status: "HTTP/1.1 200 OK"}}
	}
	href := "/" + strings.TrimPrefix(clean, "/")
	href = escapeHref(href)
	prop := prop{
		Displayname:  path.Base(strings.TrimSuffix(clean, "/")),
		ResourceType: resourceType{},
		ContentType:  "application/pdf",
	}
	if strings.HasSuffix(clean, "/") {
		href = strings.TrimSuffix(href, "/") + "/"
		prop.ResourceType.Collection = &struct{}{}
		prop.ContentType = "httpd/unix-directory"
	}
	return propResponse{Href: href, Propstat: propstat{Prop: prop, Status: "HTTP/1.1 200 OK"}}
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

func (s *Server) resolveVaultPathForCreate(rawPath string) (string, string, os.FileInfo, error) {
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
	if clean == "" {
		return "", "", nil, errors.New("root collection")
	}
	root, err := filepath.Abs(s.cfg.VaultDir)
	if err != nil {
		return "", "", nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", nil, err
	}
	components := strings.Split(clean, "/")
	target := root
	for index, component := range components {
		target = filepath.Join(target, component)
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) && index == len(components)-1 {
				return clean, target, nil, nil
			}
			return "", "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", nil, errors.New("symlink target")
		}
		if index == len(components)-1 {
			return clean, target, info, nil
		}
	}
	return "", "", nil, errors.New("invalid path")
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

func (s *Server) debug(msg string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Debug(msg, args...)
	}
}

func (s *Server) error(msg string, args ...any) {
	if s != nil && s.logger != nil {
		s.logger.Error(msg, args...)
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
	ContentLength int64        `xml:"D:getcontentlength,omitempty"`
	ContentType   string       `xml:"D:getcontenttype,omitempty"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}
