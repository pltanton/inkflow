package webdavserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// maxRewriteBody caps how much of a proxied response body upstream holds in
// memory to rewrite hrefs in; larger bodies are streamed through untouched.
const maxRewriteBody = 32 << 20

// upstream proxies non-intercepted requests to a real WebDAV server and PUTs
// intercepted uploads there too, after inkflow has imported them locally.
//
// prefix is the upstream path that inkflow's own root maps to: a request for
// inkflow path P is served from upstream path prefix+P, and upstream
// response hrefs/Location/Destination under prefix are rewritten back to P
// so the BOOX (talking only to inkflow's origin) never sees it.
type upstream struct {
	base   *url.URL
	origin string
	prefix string
	user   string
	pass   string
	client *http.Client
	proxy  *httputil.ReverseProxy
}

func newUpstream(cfg config.UpstreamConfig) (*upstream, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("upstream.url %q: %w", cfg.URL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("upstream.url %q: must be an absolute URL", cfg.URL)
	}
	u := &upstream{
		base:   base,
		origin: base.Scheme + "://" + base.Host,
		prefix: cfg.Prefix,
		user:   cfg.User,
		pass:   cfg.Password,
		client: &http.Client{Timeout: 60 * time.Second},
	}
	u.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = base.Scheme
			pr.Out.URL.Host = base.Host
			pr.Out.URL.Path = u.prefix + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = base.Host
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

// rewriteLocationLike rewrites an absolute inkflow-origin URL "origin+P" (as
// sent in a Destination request header) into "upstream-origin+prefix+P".
func (u *upstream) rewriteLocationLike(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	parsed.Scheme = u.base.Scheme
	parsed.Host = u.base.Host
	parsed.Path = u.prefix + parsed.Path
	parsed.RawPath = ""
	return parsed.String(), nil
}

// hrefValueRe matches the text content of a WebDAV href/Location-ish element
// regardless of its XML namespace prefix (D:href, d:href, or bare href).
var hrefValueRe = regexp.MustCompile(`(?is)(<[A-Za-z0-9_]*:?href[^>]*>)(.*?)(</[A-Za-z0-9_]*:?href>)`)

// rewriteResponse strips the upstream prefix (and upstream origin, if
// present) from the Location header and from every href in an XML body, so
// the BOOX sees paths relative to inkflow's own root.
func (u *upstream) rewriteResponse(resp *http.Response) error {
	if u.prefix == "" {
		return nil
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if rewritten, changed := u.stripPrefixFromValue(loc); changed {
			resp.Header.Set("Location", rewritten)
		}
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
		// Too large to safely buffer and rewrite: pass the bytes already
		// read plus whatever remains through untouched.
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

// stripPrefixFromValue strips prefix (optionally preceded by the upstream
// origin) from an href/Location value, reporting whether it matched.
func (u *upstream) stripPrefixFromValue(v string) (string, bool) {
	candidate := v
	if strings.HasPrefix(candidate, u.origin) {
		withoutOrigin := strings.TrimPrefix(candidate, u.origin)
		if strings.HasPrefix(withoutOrigin, u.prefix) {
			candidate = withoutOrigin
		}
	}
	if !strings.HasPrefix(candidate, u.prefix) {
		return v, false
	}
	rest := strings.TrimPrefix(candidate, u.prefix)
	if rest == "" {
		rest = "/"
	}
	return rest, true
}

// uploadPath is the upstream path for an intercepted PUT at inkflow path
// clean (a cleanPath result, no leading slash).
func (u *upstream) uploadPath(clean string) string {
	return u.prefix + "/" + clean
}

// putWithRetry PUTs data to path. On a 409 Conflict it MKCOLs every missing
// parent collection under the configured prefix and retries once.
func (u *upstream) putWithRetry(ctx context.Context, path string, data []byte, contentType string) error {
	status, err := u.put(ctx, path, data, contentType)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		for _, dir := range parentCollections(u.prefix, path) {
			_, _ = u.mkcol(ctx, dir)
		}
		status, err = u.put(ctx, path, data, contentType)
		if err != nil {
			return err
		}
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("upstream put %s: status %d", path, status)
	}
	return nil
}

func (u *upstream) put(ctx context.Context, path string, data []byte, contentType string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.base.String()+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(u.user, u.pass)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return u.do(req)
}

func (u *upstream) mkcol(ctx context.Context, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "MKCOL", u.base.String()+path, nil)
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

// parentCollections lists the ancestor collections of fullPath, from
// shallowest to deepest, up to (but not including) fullPath itself, and
// stopping at prefix — the caller assumes prefix itself already exists.
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
