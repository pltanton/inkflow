package webdavserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"inkflow/internal/config"
)

const maxRewriteBody = 32 << 20

// forwardedPutHeaders: lock tokens and Nextcloud's client-supplied mtime/checksum.
var forwardedPutHeaders = []string{"If", "X-OC-Mtime", "OC-Checksum"}

type upstream struct {
	base          *url.URL
	origin        string
	prefix        string
	prefixEscaped string
	user          string
	pass          string
	client        *http.Client
	proxy         *httputil.ReverseProxy
	logger        *slog.Logger
}

func newUpstream(cfg config.UpstreamConfig, logger *slog.Logger) (*upstream, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("upstream.url %q: %w", cfg.URL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("upstream.url %q: must be an absolute URL", cfg.URL)
	}
	u := &upstream{
		base:          base,
		origin:        base.Scheme + "://" + base.Host,
		prefix:        cfg.Prefix,
		prefixEscaped: (&url.URL{Path: cfg.Prefix}).EscapedPath(),
		user:          cfg.User,
		pass:          cfg.Password,
		client:        &http.Client{Timeout: 60 * time.Second},
		logger:        logger,
	}
	u.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			clean := cleanPath(pr.In.URL.Path)
			reqPath := "/" + clean
			if clean != "" && strings.HasSuffix(pr.In.URL.Path, "/") {
				reqPath += "/"
			}
			target := u.targetURL(u.prefix + reqPath)
			target.RawQuery = pr.In.URL.RawQuery
			pr.Out.URL = target
			pr.Out.Host = base.Host
			pr.Out.Header.Del("Accept-Encoding")
			pr.Out.SetBasicAuth(u.user, u.pass)
			if dest := pr.In.Header.Get("Destination"); dest != "" {
				if rewritten, err := u.rewriteLocationLike(dest); err == nil {
					pr.Out.Header.Set("Destination", rewritten)
				}
			}
		},
		ModifyResponse: u.rewriteResponse,
	}
	return u, nil
}

// targetURL builds the upstream URL for the decoded, prefixed path p,
// keeping RawPath empty so Go re-escapes it (avoids mis-parsing '#'/'?'/'%').
func (u *upstream) targetURL(p string) *url.URL {
	out := *u.base
	out.Path = strings.TrimSuffix(u.base.Path, "/") + p
	out.RawPath = ""
	out.RawQuery = ""
	return &out
}

func (u *upstream) rewriteLocationLike(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	target := u.targetURL(u.prefix + parsed.Path)
	target.RawQuery = parsed.RawQuery
	return target.String(), nil
}

var hrefValueRe = regexp.MustCompile(`(?is)(<[A-Za-z0-9_]*:?href[^>]*>)(.*?)(</[A-Za-z0-9_]*:?href>)`)

func (u *upstream) rewriteResponse(resp *http.Response) error {
	if u.prefix == "" {
		return nil
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if rewritten, changed := u.stripPrefixFromValue(loc); changed {
			resp.Header.Set("Location", rewritten)
		}
	}
	if resp.StatusCode != http.StatusMultiStatus || resp.Request.Method == http.MethodHead {
		return nil
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "xml") {
		return nil
	}

	limited := io.LimitReader(resp.Body, maxRewriteBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(body) > maxRewriteBody {
		u.warn("207 body exceeds rewrite cap, passing through unrewritten", "cap", maxRewriteBody)
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), resp.Body))
		return nil
	}
	_ = resp.Body.Close()

	rewritten := hrefValueRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := hrefValueRe.FindSubmatch(m)
		open, value, closeTag := sub[1], sub[2], sub[3]
		newValue, changed := u.stripPrefixFromValue(string(value))
		if !changed {
			return m
		}
		out := make([]byte, 0, len(open)+len(newValue)+len(closeTag))
		out = append(out, open...)
		out = append(out, newValue...)
		out = append(out, closeTag...)
		return out
	})

	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

func (u *upstream) stripPrefixFromValue(v string) (string, bool) {
	if withoutOrigin := strings.TrimPrefix(v, u.origin); withoutOrigin != v {
		if rest, ok := u.trimPrefixBoundary(withoutOrigin); ok {
			return orRoot(rest), true
		}
	}
	if rest, ok := u.trimPrefixBoundary(v); ok {
		return orRoot(rest), true
	}
	return v, false
}

func orRoot(rest string) string {
	if rest == "" {
		return "/"
	}
	return rest
}

// Compares against both prefix forms (servers may or may not escape chars
// like " ") and normalizes %xx case, since servers disagree on hex case.
func (u *upstream) trimPrefixBoundary(candidate string) (string, bool) {
	for _, p := range []string{u.prefix, u.prefixEscaped} {
		if p == "" || len(candidate) < len(p) {
			continue
		}
		if normalizePercentHex(candidate[:len(p)]) != normalizePercentHex(p) {
			continue
		}
		rest := candidate[len(p):]
		if rest == "" || rest[0] == '/' {
			return rest, true
		}
	}
	return "", false
}

func normalizePercentHex(s string) string {
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' && isHex(b[i+1]) && isHex(b[i+2]) {
			b[i+1] = upperHex(b[i+1])
			b[i+2] = upperHex(b[i+2])
		}
	}
	return string(b)
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func upperHex(c byte) byte {
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 'A'
	}
	return c
}

func (u *upstream) uploadPath(clean string) string {
	return u.prefix + "/" + clean
}

func (u *upstream) putWithRetry(ctx context.Context, path string, data []byte, header http.Header) (int, error) {
	status, err := u.put(ctx, path, data, header)
	if err != nil {
		return 0, err
	}
	if status == http.StatusConflict {
		for _, dir := range parentCollections(u.prefix, path) {
			_, _ = u.mkcol(ctx, dir)
		}
		status, err = u.put(ctx, path, data, header)
		if err != nil {
			return 0, err
		}
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("upstream put %s: status %d", path, status)
	}
	return status, nil
}

func (u *upstream) put(ctx context.Context, path string, data []byte, header http.Header) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.targetURL(path).String(), bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(u.user, u.pass)
	if ct := header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, h := range forwardedPutHeaders {
		if v := header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	return u.do(req)
}

func (u *upstream) mkcol(ctx context.Context, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", u.targetURL(path).String(), nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(u.user, u.pass)
	return u.do(req)
}

func (u *upstream) do(req *http.Request) (int, error) {
	resp, err := u.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (u *upstream) warn(msg string, args ...any) {
	if u != nil && u.logger != nil {
		u.logger.Warn(msg, args...)
	}
}

func parentCollections(prefix, fullPath string) []string {
	dir := path.Dir(fullPath)
	rel := strings.TrimPrefix(dir, prefix)
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil
	}
	segs := strings.Split(rel, "/")
	base := strings.TrimSuffix(prefix, "/")
	out := make([]string, 0, len(segs))
	cur := base
	for _, seg := range segs {
		cur += "/" + seg
		out = append(out, cur)
	}
	return out
}
