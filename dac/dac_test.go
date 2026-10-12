package dac

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/configtxt"
	"github.com/b0bbywan/odioctl/input"
)

const fixture = `# For more options and information see
# http://rptl.io/configtxt
dtparam=i2c_arm=on
dtparam=audio=on
camera_auto_detect=1

[cm4]
otg_mode=1

[pi4]
arm_boost=1

[all]
enable_uart=1
`

func entry(t *testing.T, id string) Entry {
	t.Helper()
	e, ok := ByID(id)
	if !ok {
		t.Fatalf("no catalog entry %q", id)
	}
	return e
}

func TestParseUnmanagedOnboard(t *testing.T) {
	p := Parse(fixture)
	if p.Current != Onboard || p.Managed {
		t.Errorf("parse = %+v", p)
	}
	if !slices.Equal(p.StrayLines, []string{"dtparam=audio=on"}) {
		t.Errorf("stray = %v", p.StrayLines)
	}
}

func TestOverlayOutsideTheCatalogIsLeftAlone(t *testing.T) {
	// An overlay is audio because the catalog says so, never because its name
	// looks like it: a vendor variant is treated exactly like a video overlay.
	for _, name := range []string{"hifiberry-amp4", "vc4-kms-v3d"} {
		if IsAudioOverlay(name) {
			t.Errorf("IsAudioOverlay(%q) = true", name)
		}
		text := "dtoverlay=" + name + "\n"
		if p := Parse(text); len(p.StrayLines) != 0 {
			t.Errorf("stray = %v", p.StrayLines)
		}
		if applied := Apply(text, entry(t, "hifiberry-dac")); strings.Contains(applied, DisabledPrefix) {
			t.Errorf("%q was disabled", name)
		}
	}
}

func TestParseUnmanagedKnownOverlay(t *testing.T) {
	p := Parse(fixture + "dtoverlay=hifiberry-dacplus-std\n")
	if p.Current != "hifiberry-dacplus-std" || p.Managed {
		t.Errorf("parse = %+v", p)
	}
}

func TestParseUnknownOverlayIsIgnored(t *testing.T) {
	p := Parse("dtoverlay=vc4-kms-v3d\ndtparam=audio=off\n")
	if p.Current != "" {
		t.Errorf("current = %q", p.Current)
	}
	if !slices.Equal(p.StrayLines, []string{"dtparam=audio=off"}) {
		t.Errorf("stray = %v", p.StrayLines)
	}
}

func TestManagedBlockWinsOverOutside(t *testing.T) {
	text := Apply(fixture+"dtoverlay=hifiberry-dac\n", entry(t, "iqaudio-dacplus"))
	p := Parse(text)
	if p.Current != "iqaudio-dacplus" || !p.Managed {
		t.Errorf("parse = %+v", p)
	}
	if len(p.StrayLines) != 0 {
		t.Errorf("stray = %v", p.StrayLines) // disabled lines are comments now
	}
}

func TestApplyPutsTheBlockFirstAndDisablesStrayLines(t *testing.T) {
	out := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if !strings.Contains(out, DisabledPrefix+"dtparam=audio=on") {
		t.Error("audio=on not disabled")
	}
	if strings.Contains(out, "\ndtparam=audio=on\n") {
		t.Error("audio=on still active")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	head := lines[:6]
	want := []string{Begin, "[all]", "dtparam=audio=off",
		"dtoverlay=hifiberry-dacplus-std", End, ""}
	if !slices.Equal(head, want) {
		t.Errorf("head = %v", head)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("missing final newline")
	}
	if !strings.Contains(out, "[pi4]\narm_boost=1\n") {
		t.Error("untouched lines not preserved verbatim")
	}
}

func TestApplyIdempotentAndSwitch(t *testing.T) {
	once := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if twice := Apply(once, entry(t, "hifiberry-dacplus-std")); twice != once {
		t.Error("not idempotent")
	}
	switched := Apply(once, entry(t, "allo-boss-dac-pcm512x-audio"))
	if strings.Count(switched, Begin) != 1 {
		t.Error("duplicate block")
	}
	if !strings.Contains(switched, "dtoverlay=allo-boss-dac-pcm512x-audio") ||
		strings.Contains(switched, "dtoverlay=hifiberry-dacplus-std") {
		t.Error("switch did not replace the overlay")
	}
}

func TestOnboardSetsAudioOnWithoutOverlay(t *testing.T) {
	out := Apply(fixture, entry(t, Onboard))
	_, block, _ := strings.Cut(out, Begin)
	if !strings.Contains(block, "dtparam=audio=on") {
		t.Error("audio should be on")
	}
	block, _, _ = strings.Cut(block, End)
	if strings.Contains(block, "dtoverlay") {
		t.Errorf("onboard block carries an overlay: %q", block)
	}
}

// vc4-kms-v3d disables snd_bcm2835's legacy HDMI card; an audio=on landing
// after it brings that dead card back, so our block goes first — which is
// also what leaves `dtparam=` on the base DTB, no overlay being open yet.
func TestAudioParamComesBeforeTheFilesOverlays(t *testing.T) {
	for _, id := range []string{Onboard, "hifiberry-dacplus-std"} {
		out := Apply(fixture+"dtoverlay=vc4-kms-v3d\n", entry(t, id))
		lines := strings.Split(out, "\n")
		audio := slices.IndexFunc(lines, func(l string) bool {
			return strings.HasPrefix(l, "dtparam=audio=")
		})
		vc4 := slices.Index(lines, "dtoverlay=vc4-kms-v3d")
		if lines[0] != Begin || audio < 0 || vc4 < 0 || audio >= vc4 {
			t.Errorf("%s: first = %q, audio at %d, vc4 at %d", id, lines[0], audio, vc4)
		}
	}
}

func TestParamsAreRendered(t *testing.T) {
	e := Entry{ID: "hifiberry-dacplus-std", Label: "X", Params: "slave"}
	if !strings.Contains(Apply("", e), "dtoverlay=hifiberry-dacplus-std,slave") {
		t.Error("params not rendered")
	}
}

func TestApplyEmptyFile(t *testing.T) {
	if out := Apply("", entry(t, "hifiberry-dac")); !strings.HasPrefix(out, Begin) {
		t.Errorf("out = %q", out)
	}
}

func TestUnapplyRestoresOriginal(t *testing.T) {
	applied := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if got := Unapply(applied); got != fixture {
		t.Errorf("got:\n%s", got)
	}
}

func TestUnapplyWithoutBlockIsNoop(t *testing.T) {
	if got := Unapply(fixture); got != fixture {
		t.Errorf("got:\n%s", got)
	}
}

// Two owners, two blocks: ours stays first, input's last, and neither's unset
// takes the other's block or restores the other's lines.
func TestTheInputBlockIsLeftAlone(t *testing.T) {
	text := fixture + "dtoverlay=gpio-ir,gpio_pin=17\n"
	both := input.ApplyIR(Apply(text, entry(t, "hifiberry-dacplus-std")), 25)
	if again := Apply(input.ApplyIR(both, 25), entry(t, "hifiberry-dacplus-std")); again != both {
		t.Errorf("not stable:\n%s", again)
	}
	if other := Apply(input.ApplyIR(text, 25), entry(t, "hifiberry-dacplus-std")); other != both {
		t.Errorf("the order of the writes shows:\n%s", other)
	}
	lines := strings.Split(strings.TrimSuffix(both, "\n"), "\n")
	if lines[0] != Begin || lines[len(lines)-1] != input.End {
		t.Errorf("first = %q, last = %q", lines[0], lines[len(lines)-1])
	}
	if p := Parse(both); p.Current != "hifiberry-dacplus-std" || !p.Managed {
		t.Errorf("dac = %+v", p)
	}
	if p := input.Parse(both); p.IRPin != 25 || !p.Managed {
		t.Errorf("input = %+v", p)
	}

	noDAC := Unapply(both)
	if noDAC != input.ApplyIR(text, 25) {
		t.Errorf("dac unset:\n%s", noDAC)
	}
	noIR := input.UnapplyIR(both)
	if noIR != Apply(text, entry(t, "hifiberry-dacplus-std")) {
		t.Errorf("input ir unset:\n%s", noIR)
	}
	if got := input.UnapplyIR(noDAC); got != text {
		t.Errorf("both unset:\n%s", got)
	}
}

func TestStatusAndRebootFlag(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, "config.txt")
	if err := os.WriteFile(cfg, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	old := configtxt.RebootFlag
	configtxt.RebootFlag = filepath.Join(d, "run", "reboot-required")
	t.Cleanup(func() { configtxt.RebootFlag = old })

	s := GetStatus(cfg)
	if !s.Supported || s.Current != Onboard || s.Managed || s.RebootRequired {
		t.Errorf("status = %+v", s)
	}
	configtxt.MarkRebootRequired()
	if !GetStatus(cfg).RebootRequired {
		t.Error("reboot flag not seen")
	}
	if s := GetStatus(filepath.Join(d, "nope")); s.Supported {
		t.Errorf("status = %+v", s)
	}
}
