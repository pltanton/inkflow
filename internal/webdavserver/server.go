package webdavserver

import (
	"context"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
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
		cfg.WebDAVUser = os.Getenv("INKFLOW_WEBDAV_USER")
	}
	if cfg.WebDAVPass == "" {
		cfg.WebDAVPass = os.Getenv("INKFLOW_WEBDAV_PASS")
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
		s.handlePropfind(w, r, clean)
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
	if ok && user == s.cfg.WebDAVUser && pass == s.cfg.WebDAVPass {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="inkflow"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, clean string) {
	if clean == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
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
