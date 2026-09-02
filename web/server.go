package web

// The HTTP end of the settings UI: routes and the server. Plain HTML forms
// (POST re-renders the page), no JavaScript, no JSON API. Runs as the target
// user (systemd --user unit). No authentication: same LAN trust model as
// odio-api. Every form carries a per-process token so a cross-site HTML form
// cannot drive the box.

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

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/netinfo"
)

const maxBody = 16 * 1024

// errBadToken is answered with a bare 403, not a re-render.
var errBadToken = errors.New("invalid or missing form token — reload the page and retry")

type handler struct {
	svc *Services
}

// NewHandler is the whole route table; the mux gives unknown paths their 404
// and a known path with the wrong verb its 405 + Allow.
func NewHandler(svc *Services) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.page)
	mux.HandleFunc("GET /index.html", h.page)
	mux.HandleFunc("GET /static/{name}", h.static)
	mux.HandleFunc("POST /components", h.form(h.setComponent))
	mux.HandleFunc("POST /components/action", h.form(h.componentAction))
	mux.HandleFunc("POST /dac", h.form(h.setDAC))
	mux.HandleFunc("POST /dac/unset", h.form(h.unsetDAC))
	mux.HandleFunc("POST /upgrade", h.form(h.startUpgrade))
	return mux
}

func (h *handler) page(w http.ResponseWriter, r *http.Request) {
	h.servePage(w, http.StatusOK, PageData{Host: hostOf(r)})
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

// -- the form actions ----------------------------------------------------
// Each returns the banner text and, for a component action, the modal to
// show with it; h.form lifts them into handlers.

type formAction func(form url.Values, host string) (string, *ActionResult, error)

func (h *handler) setComponent(form url.Values, _ string) (string, *ActionResult, error) {
	kind, err := formKind(form)
	if err != nil {
		return "", nil, err
	}
	msg, err := h.svc.SetComponent(kind, form.Get("name"), form.Get("enabled") == "1")
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
	return h.svc.RunAction(kind, form.Get("name"), form.Get("action"), host)
}

func (h *handler) setDAC(form url.Values, _ string) (string, *ActionResult, error) {
	id := form.Get("id")
	if id == "" { // an empty select is not an unset — that is /dac/unset
		return "", nil, userErrorf("no DAC selected")
	}
	msg, err := h.svc.SetDAC(id)
	return msg, nil, err
}

func (h *handler) unsetDAC(url.Values, string) (string, *ActionResult, error) {
	msg, err := h.svc.UnsetDAC()
	return msg, nil, err
}

func (h *handler) startUpgrade(url.Values, string) (string, *ActionResult, error) {
	msg, err := h.svc.StartUpgrade()
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

func (h *handler) servePage(w http.ResponseWriter, code int, p PageData) {
	body, err := RenderPage(h.svc, p)
	if err != nil {
		sendStatus(w, http.StatusInternalServerError, "")
		return
	}
	sendHTML(w, code, body)
}

// form lifts a formAction into a handler: token-checked form in, re-rendered
// page out — an error becomes the banner, a *UserError brings its modal.
func (h *handler) form(action formAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form, err := h.readForm(r)
		if err != nil {
			if errors.Is(err, errBadToken) {
				sendStatus(w, http.StatusForbidden, "<p>"+errBadToken.Error()+"</p>")
				return
			}
			h.servePage(w, http.StatusOK, PageData{Error: err.Error(), Host: hostOf(r)})
			return
		}
		msg, result, err := action(form, hostOf(r))
		if err != nil {
			p := PageData{Error: err.Error(), Host: hostOf(r)}
			var ue *UserError
			if errors.As(err, &ue) {
				p.Result = ue.Modal
			}
			h.servePage(w, http.StatusOK, p)
			return
		}
		h.servePage(w, http.StatusOK, PageData{Message: msg, Result: result, Host: hostOf(r)})
	}
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
	if subtle.ConstantTimeCompare([]byte(form.Get("token")), []byte(h.svc.Token())) != 1 {
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
	return serveUntilSignal(stderr, ln, NewHandler(NewServices(cfg, Runners{})))
}

func serveUntilSignal(stderr io.Writer, ln net.Listener, h http.Handler) int {
	srv := &http.Server{Handler: h}
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
