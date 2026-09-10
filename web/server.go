package web

// The HTTP end of the settings UI: routes and the server. HTML forms over
// htmx, no JSON API: a POST is answered with the notice and the state
// follows on /events, the stream every section listens to; GET / is the
// only page render. Runs as the target user (systemd --user unit). No
// authentication: same LAN trust model as odio-api. Every form carries a
// per-process token so a cross-site HTML form cannot drive the box.

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
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
	body, err := RenderPage(h.app, hostOf(r))
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

// events is the stream the page connects to on load (sse-connect on the
// body) and keeps: every section at once (the page may be stale by the
// time it connects), then only what each change named. Subscribed before
// the first send, so a change during it is not lost. A comment every 15s
// keeps idle proxies from dropping the stream.
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

// componentAction passes `host` along — the name the browser reached the box
// by becomes the OAuth callback host, so the redirect lands here and not on
// the box's loopback.
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
		msg, result, err := action(form, hostOf(r))
		if err != nil {
			h.app.log.Printf("POST %s from %s: error: %v", r.URL.Path, r.RemoteAddr, err)
			var ue *UserError
			if errors.As(err, &ue) {
				result = ue.Modal
			}
			notice(w, http.StatusOK, "", err.Error(), result)
			return
		}
		h.app.log.Printf("POST %s from %s: %s", r.URL.Path, r.RemoteAddr, msg)
		notice(w, http.StatusOK, msg, "", result)
	}
}

// reboot answers before it acts: the box goes down the moment logind takes
// the request, this connection with it, so the notice has to be on the
// wire first. A refusal reaches the page on the banners, through the stream.
func (h *handler) reboot(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.checkedForm(w, r); !ok {
		return
	}
	h.app.log.Printf("POST /reboot from %s: rebooting", r.RemoteAddr)
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
		h.app.log.Printf("POST %s from %s: %v", r.URL.Path, r.RemoteAddr, err)
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

// hostOf is the hostname the client used (for the odio-ui link), without the
// port.
func hostOf(r *http.Request) string {
	host := r.Host
	if strings.HasPrefix(host, "[") { // IPv6 literal
		return strings.SplitAfter(host, "]")[0]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// RunServe serves until SIGTERM/SIGINT, on the socket systemd passed when
// there is one, binding for itself otherwise.
func RunServe(stdout, stderr io.Writer, cfg Config) int {
	ln, err := SystemdListener()
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "odioctl web: %v\n", err)
		return 2
	case ln != nil:
		port := ln.Addr().(*net.TCPAddr).Port
		fmt.Fprintf(stdout, "Serving odioctl web UI on the socket passed by systemd (port %d)\n", port)
	default:
		if ln, err = net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)); err != nil {
			fmt.Fprintf(stderr, "odioctl web: %v\n", err)
			return 2
		}
		ip := cfg.Bind
		if ip == "0.0.0.0" || ip == "::" {
			if ip = netinfo.DefaultRouteIP(); ip == "" {
				ip = "127.0.0.1"
			}
		}
		fmt.Fprintf(stdout, "Serving odioctl web UI on http://%s:%d\n", ip, cfg.Port)
	}
	return serveUntilSignal(stderr, ln, NewHandler(NewApp(cfg, Runners{Log: stderr})))
}

func serveUntilSignal(stderr io.Writer, ln net.Listener, h http.Handler) int {
	// A phone that sleeps mid-request or a stray `nc` on port 8021 must not
	// pin a goroutine and an fd for the life of the process: bound every
	// phase of a connection. The handlers themselves are quick (a POST
	// forks and returns), so 30s of request time is generous.
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "odioctl web: %v\n", err)
		return 1
	}
	return 0
}
