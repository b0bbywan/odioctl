package web

// The HTTP end of the settings UI: routes and the server. A POST is answered
// with the notice, the state follows on /events; GET / is the only page
// render. No authentication (same LAN trust as odio-api), but every form
// carries a per-process token so a cross-site HTML form cannot drive odio.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/netinfo"
)

const maxBody = 16 * 1024

// errBadToken is answered with a 403 carrying the error banner.
var errBadToken = errors.New("invalid or missing form token — reload the page and retry")

type handler struct {
	app *App
}

// NewHandler is the whole route table; the mux gives unknown paths their 404
// and a known path with the wrong verb its 405 + Allow.
func NewHandler(app *App) http.Handler {
	h := &handler{app: app}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.page)
	mux.HandleFunc("GET /index.html", h.page)
	mux.HandleFunc("GET /static/{name}", h.static)
	mux.HandleFunc("GET /events", h.events)
	mux.HandleFunc("POST /components", h.form(h.setComponent))
	mux.HandleFunc("POST /components/action", h.form(h.componentAction))
	mux.HandleFunc("POST /dac", h.form(h.setDAC))
	mux.HandleFunc("POST /dac/unset", h.form(h.unsetDAC))
	mux.HandleFunc("POST /upgrade", h.form(h.startUpgrade))
	mux.HandleFunc("POST /reboot", h.reboot)
	return mux
}

func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	body, err := RenderPage(h.app, originOf(r))
	if err != nil {
		sendStatus(w, http.StatusInternalServerError, "")
		return
	}
	sendHTML(w, http.StatusOK, body)
}

func (h *handler) static(w http.ResponseWriter, r *http.Request) {
	content, ctype, ok := StaticAsset(r.PathValue("name"))
	if !ok {
		sendStatus(w, http.StatusNotFound, "")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(content)
}

// events is the stream the page opens on load and keeps: every section at
// once (it may be stale by then), afterwards only what a change named.
// Subscribed before the first send, so a change during it is not lost.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		sendStatus(w, http.StatusInternalServerError, "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	sub := h.app.changes.Subscribe()
	defer h.app.changes.Unsubscribe(sub)
	h.sendFragments(w, flusher, SectionNames())
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Wake():
			h.sendFragments(w, flusher, sub.Take())
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (h *handler) sendFragments(w io.Writer, flusher http.Flusher, names []string) {
	fragments, err := RenderFragments(h.app, names)
	if err != nil {
		h.app.log.Printf("events: %v", err)
	}
	for _, f := range fragments {
		fmt.Fprintf(w, "event: %s\n", f.Event)
		for _, line := range strings.Split(strings.TrimSpace(f.HTML), "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
		fmt.Fprint(w, "\n")
	}
	flusher.Flush()
}

// -- the form actions ----------------------------------------------------
// Each returns the banner text and, for a component action, the modal to
// show with it; h.form lifts them into handlers.

type formAction func(form url.Values, host string) (string, *ActionResult, error)

func (h *handler) setComponent(form url.Values, _ string) (string, *ActionResult, error) {
	kind, err := formKind(form)
	if err != nil {
		return "", nil, err
	}
	msg, err := h.app.SetComponent(kind, form.Get("name"), form.Get("enabled") == "1")
	return msg, nil, err
}

// componentAction passes `host` along — the name the browser reached odio
// by becomes the OAuth callback host, so the redirect lands here and not on
// odio's loopback.
func (h *handler) componentAction(form url.Values, host string) (string, *ActionResult, error) {
	kind, err := formKind(form)
	if err != nil {
		return "", nil, err
	}
	return h.app.RunAction(kind, form.Get("name"), form.Get("action"), host)
}

func (h *handler) setDAC(form url.Values, _ string) (string, *ActionResult, error) {
	id := form.Get("id")
	if id == "" { // an empty select is not an unset — that is /dac/unset
		return "", nil, userErrorf("no DAC selected")
	}
	msg, err := h.app.SetDAC(id)
	return msg, nil, err
}

func (h *handler) unsetDAC(url.Values, string) (string, *ActionResult, error) {
	msg, err := h.app.UnsetDAC()
	return msg, nil, err
}

func (h *handler) startUpgrade(url.Values, string) (string, *ActionResult, error) {
	msg, err := h.app.upgrades.Start(h.app.UpgradeReport())
	return msg, nil, err
}

// formKind narrows the request's kind field to the catalog's two kinds.
func formKind(form url.Values) (components.Kind, error) {
	kind := form.Get("kind")
	if kind != string(components.Role) && kind != string(components.Feature) {
		return "", userErrorf("unknown component kind %q", kind)
	}
	return components.Kind(kind), nil
}

func sendHTML(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// sendStatus is the minimal <h1>-only page for the 403/404/405 dead ends.
func sendStatus(w http.ResponseWriter, code int, detail string) {
	sendHTML(w, code, fmt.Sprintf("<h1>%d</h1>%s", code, detail))
}

// notice answers a POST: the banner for #notice, the modal of an action
// out of band; the page's sections follow on /events.
func notice(w http.ResponseWriter, code int, msg, errText string, modal *ActionResult) {
	body, err := RenderNotice(msg, errText, modal)
	if err != nil {
		sendStatus(w, http.StatusInternalServerError, "")
		return
	}
	sendHTML(w, code, body)
}

// form lifts a formAction into a handler: token-checked form in, the
// notice out — an error becomes the banner, a *UserError brings its modal.
func (h *handler) form(action formAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form, ok := h.checkedForm(w, r)
		if !ok {
			return
		}
		o := originOf(r)
		msg, result, err := action(form, o.Host)
		if err != nil {
			h.app.log.Printf("POST %s from %s: error: %v", r.URL.Path, o.Remote, err)
			var ue *UserError
			if errors.As(err, &ue) {
				result = ue.Modal
			}
			notice(w, http.StatusOK, "", err.Error(), result)
			return
		}
		h.app.log.Printf("POST %s from %s: %s", r.URL.Path, o.Remote, msg)
		notice(w, http.StatusOK, msg, "", result)
	}
}

// reboot answers before it acts: odio goes down the moment logind takes
// the request, this connection with it, so the notice has to be on the
// wire first. A refusal reaches the page on the banners, through the stream.
func (h *handler) reboot(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.checkedForm(w, r); !ok {
		return
	}
	h.app.log.Printf("POST /reboot from %s: rebooting", originOf(r).Remote)
	notice(w, http.StatusOK, "Rebooting — odio will be back soon.", "", nil)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	h.app.Reboot()
}

// checkedForm is the prologue of every POST: the token-checked form, or
// the error notice already written and false.
func (h *handler) checkedForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	form, err := h.readForm(r)
	if err != nil {
		h.app.log.Printf("POST %s from %s: %v", r.URL.Path, originOf(r).Remote, err)
		code := http.StatusOK
		if errors.Is(err, errBadToken) {
			code = http.StatusForbidden
		}
		notice(w, code, "", err.Error(), nil)
		return nil, false
	}
	return form, true
}

func (h *handler) readForm(r *http.Request) (url.Values, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return nil, errors.New("expected a form submission")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return nil, errors.New("form too large")
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, errors.New("cannot parse the form")
	}
	if subtle.ConstantTimeCompare([]byte(form.Get("token")), []byte(h.app.Token())) != 1 {
		return nil, errBadToken
	}
	return form, nil
}

// origin is how the browser reached odio: on port 8021, or through odio-api's
// reverse proxy on the Unix socket — the only door whose X-Forwarded-* is
// believed, since only the target user can connect to it.
type origin struct {
	Host   string // the hostname, no port: the odio-ui link, an action's {host}
	Base   string // the page's <base href>, "/" unless proxied under a prefix
	Remote string // who, for the log
}

type viaProxyKey struct{}

// markProxied is the server's ConnContext: it tags the Unix socket's
// connections for originOf.
func markProxied(ctx context.Context, c net.Conn) context.Context {
	if _, ok := c.(*net.UnixConn); ok {
		return context.WithValue(ctx, viaProxyKey{}, true)
	}
	return ctx
}

func originOf(r *http.Request) origin {
	o := origin{Host: hostOf(r.Host), Base: "/", Remote: r.RemoteAddr}
	if proxied, _ := r.Context().Value(viaProxyKey{}).(bool); !proxied {
		return o
	}
	o.Remote = "proxy"
	if h := lastValue(r.Header.Get("X-Forwarded-Host")); h != "" {
		o.Host = hostOf(h)
	}
	if ip := lastValue(r.Header.Get("X-Forwarded-For")); ip != "" {
		o.Remote = ip
	}
	o.Base = basePath(r.Header.Get("X-Forwarded-Prefix"))
	return o
}

// lastValue is the hop nearest to us in a comma-separated X-Forwarded-*.
func lastValue(v string) string {
	return strings.TrimSpace(v[strings.LastIndex(v, ",")+1:])
}

// basePath turns X-Forwarded-Prefix into a <base href>: a clean absolute
// path of plain characters ending in "/", "/" for anything else. Clean also
// folds a leading "//", which would point the page's scripts at another host.
func basePath(prefix string) string {
	if !strings.HasPrefix(prefix, "/") || strings.ContainsFunc(prefix, notPathChar) {
		return "/"
	}
	if p := path.Clean(prefix); p != "/" {
		return p + "/"
	}
	return "/"
}

func notPathChar(c rune) bool {
	return (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("/-._~", c)
}

// hostOf is a Host header's hostname, without the port.
func hostOf(host string) string {
	if strings.HasPrefix(host, "[") { // IPv6 literal
		return strings.SplitAfter(host, "]")[0]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// RunServe serves until SIGTERM/SIGINT, on the sockets systemd passed when
// there are any, binding for itself otherwise.
func RunServe(stdout, stderr io.Writer, cfg Config) int {
	lns, err := SystemdListeners()
	source := " (passed by systemd)"
	if err == nil && lns == nil && cfg.SystemdOnly {
		// A bare `systemctl start` must not open a door odios left closed.
		err = errNoSockets
	}
	if err == nil && lns == nil {
		lns, err = bind(cfg)
		source = ""
	}
	if err != nil {
		fmt.Fprintf(stderr, "odioctl web: %v\n", err)
		return 2
	}
	for _, ln := range lns {
		fmt.Fprintf(stdout, "Serving odioctl web UI on %s%s\n", describe(ln), source)
	}
	return serveUntilSignal(stderr, lns, NewHandler(NewApp(cfg, Runners{Log: stderr})))
}

var errNoSockets = errors.New("--systemd-only, but systemd passed no socket: " +
	"start odioctl-web.socket or odioctl-web-proxy.socket, not the service")

// bind is RunServe without systemd: the TCP port, plus the Unix socket
// --socket names. All or nothing.
func bind(cfg Config) ([]net.Listener, error) {
	tcp, err := net.Listen("tcp", net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, err
	}
	if cfg.Socket == "" {
		return []net.Listener{tcp}, nil
	}
	unix, err := listenUnix(cfg.Socket)
	if err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return []net.Listener{tcp, unix}, nil
}

// describe is where a listener is reached: the socket's path, or a URL —
// on the LAN address when the port is bound to all of them.
func describe(ln net.Listener) string {
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return ln.Addr().String()
	}
	ip := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		if ip = netinfo.DefaultRouteIP(); ip == "" {
			ip = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(tcp.Port))
}

func serveUntilSignal(stderr io.Writer, lns []net.Listener, h http.Handler) int {
	srv := newServer(h)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		_ = srv.Close()
	}()
	// The first listener to stop takes the others with it: a signal, or a
	// failure that leaves odio with half its doors.
	done := make(chan error, len(lns))
	for _, ln := range lns {
		go func() { done <- srv.Serve(ln) }()
	}
	err := <-done
	_ = srv.Close()
	if !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "odioctl web: %v\n", err)
		return 1
	}
	return 0
}

func newServer(h http.Handler) *http.Server {
	// A sleeping phone or a stray `nc` on 8021 must not pin a goroutine and an
	// fd for the life of the process: bound every phase. Handlers are quick.
	return &http.Server{
		Handler:           h,
		ConnContext:       markProxied,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
