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

func TestApplyPutsTheBlockFirstAndDisablesStrayLines(t *testing.T) {
	out := Apply(fixture, entry(t, "hifiberry-dacplus-std"))
	if !strings.Contains(out, DisabledPrefix+"dtparam=audio=on") {
		t.Error("audio=on not disabled")
	}
	if strings.Contains(out, "\ndtparam=audio=on\n") {
		t.Error("audio=on still active")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	// The empty dtoverlay= ahead of any other directive suppresses a HAT's
	// EEPROM overlay, which the firmware loads before config.txt.
	head := lines[:7]
	want := []string{Begin, "[all]", "dtoverlay=", "dtparam=audio=off",
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

// The Pi's own output is what Reset gives back, never a choice: a block can
// only turn it off.
func TestOnboardIsNotAChoice(t *testing.T) {
	if _, ok := ByID(Onboard); ok {
		t.Error("onboard is in the catalog")
	}
	if strings.Contains(Apply(fixture, entry(t, "hifiberry-dac")), "\ndtparam=audio=on\n") {
		t.Error("a block turned audio on")
	}
}

// odioctl 0.4.0 wrote an onboard block: still read, and Reset removes it.
func TestAnEarlierOnboardBlockIsReadAndReset(t *testing.T) {
	text := Begin + "\n[all]\ndtparam=audio=on\n" + End + "\n\n" + "camera_auto_detect=1\n"
	if p := Parse(text); p.Current != Onboard || !p.Managed {
		t.Errorf("parse = %+v", p)
	}
	if got := Unapply(text); got != "camera_auto_detect=1\n" {
		t.Errorf("unapply = %q", got)
	}
}

// vc4-kms-v3d disables snd_bcm2835's legacy HDMI card; an audio=on landing
// after it brings that dead card back, so our block goes first.
func TestAudioParamComesBeforeTheFilesOverlays(t *testing.T) {
	out := Apply(fixture+"dtoverlay=vc4-kms-v3d\n", entry(t, "hifiberry-dacplus-std"))
	lines := strings.Split(out, "\n")
	audio := slices.Index(lines, "dtparam=audio=off")
	vc4 := slices.Index(lines, "dtoverlay=vc4-kms-v3d")
	if lines[0] != Begin || audio < 0 || vc4 < 0 || audio >= vc4 {
		t.Errorf("first = %q, audio at %d, vc4 at %d", lines[0], audio, vc4)
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
