package web

// The page: view models built from the App, markup and composition in
// templates/*.gohtml, look and htmx copies from odio-ui in static/. Each
// section swaps itself on its own /events event; a POST answers the notice.

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

//go:embed templates/*.gohtml
var templatesFS embed.FS

//go:embed static/style.css static/logo.png static/htmx.min.js static/htmx-sse.js
var staticFS embed.FS

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.gohtml"))

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
	Status                    string // picks the icon pair, and its colours
	Title                     string // the state and what a click does: title/aria-label
	Required                  bool   // a lock, where the toggle would be
	Token, Kind, Name, Enable string // the toggle form
	Actions                   []actionView
}

type groupView struct {
	Title string
	Rows  []rowView
}

type componentsView struct {
	Err    string // non-empty → error banner instead of content
	Groups []groupView
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
	Version, Base, UIURL, Hostname string
	Odios                          string          // "" = no badge
	Sections                       []template.HTML // each rendered by its own template, in sections order
}

// noticeView is the outcome of a POST: its banner, and the modal of an
// action — the fragment for #notice, with the modal swapped out of band
// into #modal.
type noticeView struct {
	Notices []bannerView
	Modal   *ActionResult
}

// The icon carries no words: its title says the state and what a click does.
// No verb: a label can be plural ("Web radios").
var statusTitle = map[components.Status]string{
	components.Installed: "%s — installed, click to disable",
	components.Excluded:  "%s — disabled, click to enable",
	components.Default:   "%s — installs on the next upgrade, click to skip",
}

func rowViewOf(app *App, c components.Component, child bool) rowView {
	title := fmt.Sprintf(statusTitle[c.Status], c.Label)
	if !c.Toggleable {
		title = c.Label + " — required by odio"
	}
	enable := "1"
	if c.Enabled() {
		enable = "0"
	}
	row := rowView{
		Child:       child,
		Label:       c.Label,
		Description: c.Description,
		Status:      string(c.Status),
		Title:       title,
		Required:    !c.Toggleable,
		Token:       app.Token(),
		Kind:        string(c.Kind),
		Name:        c.Name,
		Enable:      enable,
	}
	// Actions are offered only once the component is installed — the command
	// they run ships with the package. The pending link (or the outcome of
	// the last run) renders under the row, next to what it belongs to.
	if c.Status != components.Installed {
		return row
	}
	for _, a := range c.Actions {
		url, note := app.actions.State(c.Kind, c.Name, a.ID)
		av := actionView{ID: a.ID, Button: a.Label, Note: note}
		if url != "" {
			av.URL, av.LinkLabel, av.LinkNote = url, a.LinkLabel, a.LinkNote
		}
		row.Actions = append(row.Actions, av)
	}
	return row
}

func componentsViewOf(app *App, st *state.State, stateErr string) componentsView {
	if st == nil {
		return componentsView{Err: "state.json: " + stateErr}
	}
	comps := components.List(*st, app.TargetManifest())
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
	for _, r := range comps {
		if r.Kind != components.Role {
			continue
		}
		rows := append(rowsByGroup[r.Group], rowViewOf(app, r, false))
		for _, f := range byParent[r.Name] {
			rows = append(rows, rowViewOf(app, f, true))
		}
		rowsByGroup[r.Group] = rows
	}
	last := components.Groups[len(components.Groups)-1]
	for _, f := range orphans {
		rowsByGroup[last] = append(rowsByGroup[last], rowViewOf(app, f, false))
	}
	var view componentsView
	for _, title := range components.Groups {
		if rows := rowsByGroup[title]; len(rows) > 0 {
			view.Groups = append(view.Groups, groupView{Title: title, Rows: rows})
		}
	}
	return view
}

func dacViewOf(app *App, d dac.Status) dacView {
	if !d.Supported {
		return dacView{}
	}
	view := dacView{
		Supported: true,
		Token:     app.Token(),
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

func upgradeViewOf(app *App, report *upgrade.Report) upgradeView {
	running, note := app.upgrades.State(report)
	view := upgradeView{Running: running, Note: note}
	if report == nil {
		return view
	}
	view.Checked = true
	view.Available = report.UpgradeAvailable
	view.Token = app.Token()
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
		view.Items = append(view.Items, "install "+components.LabelOf(&report.Manifest, components.Kind(kind), name))
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
// waits for, with the button that does it, and the refusal of the last one.
type bannersView struct {
	RebootRequired bool
	RebootError    string
	Token          string
}

func bannersOf(app *App, d dac.Status) bannersView {
	return bannersView{RebootRequired: d.RebootRequired, RebootError: app.RebootError(), Token: app.Token()}
}

// RenderNotice is the answer to a POST: the banner for #notice and, out of
// band, the modal of an action. The state follows on the stream.
func RenderNotice(msg, errText string, modal *ActionResult) (string, error) {
	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, "notice.gohtml", noticeViewOf(msg, errText, modal)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Section is one piece of the page and one event on the stream: its
// template, <Name>.gohtml, has a root listening to Name (sse-swap) that
// swaps itself. sections is the page's order.
type Section struct {
	Name string
	view func(app *App) any
}

var sections = []Section{
	{"banners", func(app *App) any { return bannersOf(app, app.DacStatus()) }},
	{"upgrade", func(app *App) any { return upgradeViewOf(app, app.UpgradeReport()) }},
	{"components", func(app *App) any { st, err := stateOf(app); return componentsViewOf(app, st, err) }},
	{"dac", func(app *App) any { return dacViewOf(app, app.DacStatus()) }},
}

// SectionNames is every section, what a stream sends first.
func SectionNames() []string {
	names := make([]string, len(sections))
	for i, sec := range sections {
		names[i] = sec.Name
	}
	return names
}

func render(name string, data any) (string, error) {
	var b strings.Builder
	err := templates.ExecuteTemplate(&b, name, data)
	return b.String(), err
}

// Fragment is one event on the stream.
type Fragment struct {
	Event, HTML string
}

// RenderFragments is what a change named: sections in the page's order,
// then the modal of a finished action by its id (only a page showing it
// listens). A name that is neither is nothing.
func RenderFragments(app *App, names []string) ([]Fragment, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Fragment
	for _, sec := range sections {
		if !want[sec.Name] {
			continue
		}
		html, err := render(sec.Name+".gohtml", sec.view(app))
		if err != nil {
			return nil, err
		}
		out = append(out, Fragment{sec.Name, html})
	}
	for _, res := range app.actions.Finished() {
		if !want[res.ID] {
			continue
		}
		html, err := render("modal.gohtml", modalView(res))
		if err != nil {
			return nil, err
		}
		out = append(out, Fragment{res.ID, html})
	}
	return out, nil
}

// stateOf is state.json as the Components section takes it: the state, or
// nil and the message to show instead.
func stateOf(app *App) (*state.State, string) {
	s, err := app.ReadState()
	if err != nil {
		return nil, stateErrorMsg(app.Config().StatePath, err)
	}
	return &s, ""
}

// RenderPage is GET /: the sections as they stand, an empty #notice and
// #modal for the POSTs to fill, every URL relative to the <base> o sets.
func RenderPage(app *App, o origin) (string, error) {
	// The Host header when the browser gave one (that name reaches odio), its
	// own hostname otherwise — same address for the odio-ui link and ssh.
	hostname := o.Host
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	uiURL := app.Config().UIURL
	if uiURL == "" {
		uiURL = fmt.Sprintf("http://%s:%d/ui", hostname, OdioUIPort)
	}
	selfName, _ := os.Hostname()

	view := pageView{
		Version:  config.AppVersion,
		Base:     o.Base,
		UIURL:    uiURL,
		Hostname: selfName,
	}
	if st, _ := stateOf(app); st != nil {
		view.Odios = st.Odios
	}
	for _, sec := range sections {
		html, err := render(sec.Name+".gohtml", sec.view(app))
		if err != nil {
			return "", err
		}
		view.Sections = append(view.Sections, template.HTML(html)) // our own template's output, already escaped
	}

	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, "page.gohtml", view); err != nil {
		return "", err
	}
	return b.String(), nil
}
