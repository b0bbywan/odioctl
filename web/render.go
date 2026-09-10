package web

// The page: view models built from Services, markup in templates/*.html —
// composition ({{range}}, {{if}}, {{template}}) lives in the templates,
// escaping in html/template. The stylesheet and logo in static/ mirror
// odio-ui's look (go-odio-api), htmx and its SSE extension are odio-api's
// copies. Each section subscribes to its own /events event and swaps
// itself; a POST answers the notice (and the modal of an action), the
// state follows on the stream.

import (
	"embed"
	"fmt"
	"html/template"
	"os"
	"strings"

	"github.com/b0bbywan/odioctl/components"
	"github.com/b0bbywan/odioctl/config"
	"github.com/b0bbywan/odioctl/dac"
	"github.com/b0bbywan/odioctl/state"
	"github.com/b0bbywan/odioctl/upgrade"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/style.css static/logo.png static/htmx.min.js static/htmx-sse.js
var staticFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

var staticTypes = map[string]string{
	"style.css":   "text/css; charset=utf-8",
	"logo.png":    "image/png",
	"htmx.min.js": "text/javascript; charset=utf-8",
	"htmx-sse.js": "text/javascript; charset=utf-8",
}

// StaticAsset is the (content, media type) of a file under static/, or ok=false.
func StaticAsset(name string) (content []byte, mediaType string, ok bool) {
	ctype, known := staticTypes[name]
	if !known {
		return nil, "", false
	}
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		return nil, "", false
	}
	return b, ctype, true
}

// -- view models ---------------------------------------------------------

type bannerView struct{ Kind, Text string }

type actionView struct {
	ID, Button               string
	URL, LinkLabel, LinkNote string     // pending link, when URL is set
	Note                     actionNote // how the last run ended: a badge, or the failure in red
}

type rowView struct {
	Child                     bool
	Label, Description        string
	Status, Chip              string
	Token, Kind, Name, Enable string // the toggle form
	Button                    string
	Actions                   []actionView
}

type groupView struct {
	Title string
	Rows  []rowView
}

type componentsView struct {
	Err              string // non-empty → error banner instead of content
	User, Mode, Note string
	Groups           []groupView
	Infra            string
}

type dacOptionView struct {
	ID, Text           string
	Disabled, Selected bool
}

type dacView struct {
	Supported      bool
	Token, Current string
	Managed        bool
	Options        []dacOptionView
	Stray          string
}

type upgradeView struct {
	Checked   bool // a check has run (upgrades.json exists)
	Available bool
	Running   bool       // odio-upgrade.service is being followed: no Apply button
	Note      actionNote // how the last run ended, empty when none was seen
	UpToDate  string
	Token     string
	Items     []string
}

type pageView struct {
	Version, UIURL, Hostname string
	Odios                    string // "" = no badge
	Banners                  bannersView
	Upgrade                  upgradeView
	Components               componentsView
	Dac                      dacView
}

// noticeView is the outcome of a POST: its banner, and the modal of an
// action — the fragment for #notice, with the modal swapped out of band
// into #modal.
type noticeView struct {
	Notices []bannerView
	Modal   *ActionResult
}

// (chip text, button label) per component status; the button performs the
// opposite action.
var statusUI = map[components.Status][2]string{
	components.Installed: {"Installed", "Disable"},
	components.Excluded:  {"Disabled", "Enable"},
	components.Default:   {"Will install on next upgrade", "Skip"},
}

func rowViewOf(svc *Services, c components.Component, child bool) rowView {
	ui := statusUI[c.Status]
	enable := "1"
	if c.Enabled() {
		enable = "0"
	}
	description := c.Description
	if description == "" {
		description = c.Name
	}
	row := rowView{
		Child:       child,
		Label:       c.Label,
		Description: description,
		Status:      string(c.Status),
		Chip:        ui[0],
		Token:       svc.Token(),
		Kind:        string(c.Kind),
		Name:        c.Name,
		Enable:      enable,
		Button:      ui[1],
	}
	// Actions are offered only once the component is installed — the command
	// they run ships with the package. The pending link (or the outcome of
	// the last run) renders under the row, next to what it belongs to.
	if c.Status != components.Installed {
		return row
	}
	for _, a := range c.Actions {
		url, note := svc.ActionState(c.Kind, c.Name, a.ID)
		av := actionView{ID: a.ID, Button: a.Label, Note: note}
		if url != "" {
			av.URL, av.LinkLabel, av.LinkNote = url, a.LinkLabel, a.LinkNote
			if av.LinkNote == "" {
				av.LinkNote = "started"
			}
		}
		row.Actions = append(row.Actions, av)
	}
	return row
}

func componentsViewOf(svc *Services, st *state.State, stateErr string) componentsView {
	if st == nil {
		return componentsView{Err: "state.json: " + stateErr}
	}
	comps := components.List(*st, svc.AvailableRoles())
	byParent := map[string][]components.Component{}
	var orphans []components.Component
	for _, f := range comps {
		if f.Kind != components.Feature {
			continue
		}
		if f.Parent != "" {
			byParent[f.Parent] = append(byParent[f.Parent], f)
		} else {
			orphans = append(orphans, f)
		}
	}
	rowsByGroup := map[string][]rowView{}
	var infra []string
	for _, r := range comps {
		if r.Kind != components.Role {
			continue
		}
		if !r.Toggleable {
			infra = append(infra, r.Label)
			continue
		}
		rows := append(rowsByGroup[r.Group], rowViewOf(svc, r, false))
		for _, f := range byParent[r.Name] {
			rows = append(rows, rowViewOf(svc, f, true))
		}
		rowsByGroup[r.Group] = rows
	}
	last := components.Groups[len(components.Groups)-1]
	for _, f := range orphans {
		rowsByGroup[last] = append(rowsByGroup[last], rowViewOf(svc, f, false))
	}
	view := componentsView{
		User: st.TargetUser,
		Mode: st.InstallMode,
		Note: components.ApplyNote,
	}
	for _, title := range components.Groups {
		if rows := rowsByGroup[title]; len(rows) > 0 {
			view.Groups = append(view.Groups, groupView{Title: title, Rows: rows})
		}
	}
	if len(infra) > 0 {
		view.Infra = "Always installed: " + strings.Join(infra, ", ")
	}
	return view
}

func dacViewOf(svc *Services, d dac.Status) dacView {
	if !d.Supported {
		return dacView{}
	}
	view := dacView{
		Supported: true,
		Token:     svc.Token(),
		Managed:   d.Managed,
		Options: []dacOptionView{
			{ID: "", Text: "— not configured —", Disabled: true, Selected: d.Current == ""},
		},
	}
	for _, e := range dac.Catalog {
		view.Options = append(view.Options, dacOptionView{
			ID:       e.ID,
			Text:     fmt.Sprintf("%s (%s)", e.Label, e.ID),
			Selected: e.ID == d.Current,
		})
	}
	switch {
	case d.Current != "" && d.Managed:
		view.Current = "Current: " + d.Current + " (managed by odioctl)"
	case d.Current != "":
		view.Current = "Current: " + d.Current + " (from existing config.txt)"
	case len(d.StrayLines) > 0:
		view.Current = "Unrecognised audio configuration in config.txt: " + strings.Join(d.StrayLines, "; ")
	default:
		view.Current = "No DAC configured"
	}
	if len(d.StrayLines) > 0 && d.Managed {
		// Unmanaged lines are simply what defines Current; once odioctl owns
		// the block, anything else left active is a conflict worth flagging.
		view.Stray = "Audio lines outside the odioctl block (commented out on Apply): " +
			strings.Join(d.StrayLines, "; ")
	}
	return view
}

func upgradeViewOf(svc *Services, report *upgrade.Report) upgradeView {
	running, note := svc.UpgradeState(report)
	view := upgradeView{Running: running, Note: note}
	if report == nil {
		return view
	}
	view.Checked = true
	view.Available = report.UpgradeAvailable
	view.Token = svc.Token()
	if !report.UpgradeAvailable {
		view.UpToDate = fmt.Sprintf("Up to date — odio %s (checked %s).",
			report.Current, report.CheckedAt)
		return view
	}
	if report.Latest != report.Current {
		view.Items = append(view.Items, fmt.Sprintf("odio %s → %s", report.Current, report.Latest))
	}
	for _, r := range report.Roles {
		view.Items = append(view.Items, fmt.Sprintf("%s %s → %s", r.Name, r.Installed, r.Available))
	}
	for _, ref := range report.PendingComponents {
		kind, name, _ := strings.Cut(ref, ":")
		view.Items = append(view.Items, "install "+components.LabelOf(components.Kind(kind), name))
	}
	return view
}

func modalView(res *ActionResult) *ActionResult {
	if res == nil {
		return nil
	}
	m := *res
	m.Output = strings.TrimSpace(m.Output)
	if m.Output == "" {
		m.Output = "(no output)"
	}
	return &m
}

func noticeViewOf(msg, errText string, modal *ActionResult) noticeView {
	view := noticeView{Modal: modalView(modal)}
	for _, b := range []bannerView{{"ok", msg}, {"err", errText}} {
		if b.Text != "" {
			view.Notices = append(view.Notices, b)
		}
	}
	return view
}

// bannersView is what holds across renders: the reboot the DAC change
// waits for, with the button that does it.
type bannersView struct {
	RebootRequired bool
	Token          string
}

func bannersOf(svc *Services, d dac.Status) bannersView {
	return bannersView{RebootRequired: d.RebootRequired, Token: svc.Token()}
}

// RenderNotice is the answer to a POST: the banner for #notice and, out of
// band, the modal of an action. The state follows on the stream.
func RenderNotice(msg, errText string, modal *ActionResult) (string, error) {
	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, "notice.html", noticeViewOf(msg, errText, modal)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Section is one /events event: the element it carries swaps itself in,
// listening to Event by name (sse-swap on its root).
type Section struct {
	Event, HTML string
}

// RenderSections is the whole state as the stream sends it on connect and
// on every change: the modal of each finished action (only a page showing
// it listens), the banners, the upgrade card, the Components section (an
// upgrade installs rows) and the DAC one — from the page's own templates.
func RenderSections(svc *Services) ([]Section, error) {
	var out []Section
	add := func(event, name string, data any) error {
		var b strings.Builder
		if err := templates.ExecuteTemplate(&b, name, data); err != nil {
			return err
		}
		out = append(out, Section{Event: event, HTML: b.String()})
		return nil
	}
	for _, res := range svc.FinishedResults() {
		if err := add(res.ID, "modal.html", modalView(res)); err != nil {
			return nil, err
		}
	}
	if err := add("banners", "banners.html", bannersOf(svc, svc.DacStatus())); err != nil {
		return nil, err
	}
	if err := add("upgrade", "upgrade.html", upgradeViewOf(svc, svc.UpgradeReport())); err != nil {
		return nil, err
	}
	st, stateErr := stateOf(svc)
	if err := add("components", "components.html", componentsViewOf(svc, st, stateErr)); err != nil {
		return nil, err
	}
	if err := add("dac", "dac.html", dacViewOf(svc, svc.DacStatus())); err != nil {
		return nil, err
	}
	return out, nil
}

// stateOf is state.json as the Components section takes it: the state, or
// nil and the message to show instead.
func stateOf(svc *Services) (*state.State, string) {
	s, err := svc.ReadState()
	if err != nil {
		return nil, stateErrorMsg(svc.Config().StatePath, err)
	}
	return &s, ""
}

// RenderPage is GET /: the sections as they stand, an empty #notice and
// #modal for the POSTs to fill. host is the Host header the browser used.
func RenderPage(svc *Services, host string) (string, error) {
	st, stateErr := stateOf(svc)
	d := svc.DacStatus()
	// The Host header when the browser gave one (that name reaches the box),
	// the box's own hostname otherwise — same address for the odio-ui link
	// and ssh. The logo is that way home: this page is a settings annex of
	// odio-ui.
	hostname := host
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	uiURL := fmt.Sprintf("http://%s:%d/ui", hostname, OdioUIPort)
	selfName, _ := os.Hostname()

	view := pageView{
		Version:    config.AppVersion,
		UIURL:      uiURL,
		Hostname:   selfName,
		Banners:    bannersOf(svc, d),
		Upgrade:    upgradeViewOf(svc, svc.UpgradeReport()),
		Components: componentsViewOf(svc, st, stateErr),
		Dac:        dacViewOf(svc, d),
	}
	if st != nil {
		view.Odios = st.Odios
	}

	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, "page.html", view); err != nil {
		return "", err
	}
	return b.String(), nil
}
