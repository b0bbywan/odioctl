package dac

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

func TestApplyAppendsBlockAndDisablesStrayLines(t *testing.T) {
	out := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if !strings.Contains(out, DisabledPrefix+"dtparam=audio=on") {
		t.Error("audio=on not disabled")
	}
	if strings.Contains(out, "\ndtparam=audio=on\n") {
		t.Error("audio=on still active")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	tail := lines[len(lines)-6:]
	want := []string{Begin, "[all]", "dtoverlay=", "dtparam=audio=off",
		"dtoverlay=hifiberry-dacplus-std", End}
	if !slices.Equal(tail, want) {
		t.Errorf("tail = %v", tail)
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
	var overlays []string
	for _, ln := range strings.Split(block, "\n") {
		if strings.Contains(ln, "dtoverlay") {
			overlays = append(overlays, ln)
		}
	}
	if !slices.Equal(overlays, []string{"dtoverlay="}) {
		t.Errorf("overlays = %v", overlays) // only the reset, never an overlay
	}
}

func TestAudioParamIsTargetedAtTheBaseDTB(t *testing.T) {
	// A `dtparam=` applies to the overlay loaded above it, so the reset must
	// come between the last `dtoverlay=` of the file and our `dtparam=audio=`.
	for _, id := range []string{Onboard, "hifiberry-dacplus-std"} {
		out := Apply(fixture+"dtoverlay=vc4-kms-v3d\n", entry(t, id))
		lines := strings.Split(out, "\n")
		vc4 := slices.Index(lines, "dtoverlay=vc4-kms-v3d")
		reset := slices.Index(lines, "dtoverlay=")
		if vc4 < 0 || reset < 0 || vc4 >= reset {
			t.Errorf("%s: vc4 at %d, reset at %d", id, vc4, reset)
		}
		if !strings.HasPrefix(lines[reset+1], "dtparam=audio=") {
			t.Errorf("%s: audio param not right below the reset", id)
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

func TestWriteConfigKeepsAOneTimeBackup(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(cfg, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if err := WriteConfig(cfg, applied); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(cfg, Apply(applied, entry(t, "hifiberry-dac"))); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(cfg + ".odioctl.bak")
	if err != nil || string(bak) != fixture {
		t.Errorf("backup = %q, %v; want the pristine file", bak, err)
	}
}

func TestStatusAndRebootFlag(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, "config.txt")
	if err := os.WriteFile(cfg, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	old := RebootFlag
	RebootFlag = filepath.Join(d, "run", "reboot-required")
	t.Cleanup(func() { RebootFlag = old })

	s := GetStatus(cfg)
	if !s.Supported || s.Current != Onboard || s.Managed || s.RebootRequired {
		t.Errorf("status = %+v", s)
	}
	MarkRebootRequired()
	if !GetStatus(cfg).RebootRequired {
		t.Error("reboot flag not seen")
	}
	if s := GetStatus(filepath.Join(d, "nope")); s.Supported {
		t.Errorf("status = %+v", s)
	}
}
