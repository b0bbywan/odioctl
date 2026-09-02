// Package dac manages the `dtoverlay=` line in the Raspberry Pi config.txt.
//
// odioctl owns one marked block at the end of config.txt:
//
//	# BEGIN odioctl dac -- managed block, edit with `odioctl dac`
//	[all]
//	dtoverlay=
//	dtparam=audio=off
//	dtoverlay=hifiberry-dacplus-std
//	# END odioctl dac
//
// The [all] resets any open [pi4]-style filter section, and the empty
// `dtoverlay=` retargets `dtparam=` at the base DTB (see overlayReset).
// Pre-existing top-level audio lines are commented out with DisabledPrefix so
// `dac unset` restores them verbatim. Parse/Apply/Unapply are pure; only
// ReadConfig/WriteConfig touch the disk. Writing needs root — unprivileged
// callers go through `sudo odioctl dac set <id>`.
package dac

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/b0bbywan/odioctl/fsutil"
)

var ConfigCandidates = []string{"/boot/firmware/config.txt", "/boot/config.txt"}

// RebootFlag is a var so tests can point it into a tempdir.
var RebootFlag = "/run/odioctl/reboot-required"

const (
	Begin = "# BEGIN odioctl dac -- managed block, edit with `odioctl dac`"
	End   = "# END odioctl dac"
	// An empty `dtoverlay=` ends the parameter list of the overlay above it:
	// without this reset our `dtparam=audio=` would land on whatever the file
	// loaded last instead of the base DTB, and snd_bcm2835 never loads.
	overlayReset   = "dtoverlay="
	DisabledPrefix = "#odioctl-disabled: "

	Onboard = "onboard"
)

type Entry struct {
	ID     string `json:"id"` // also the dtoverlay name
	Label  string `json:"label"`
	Params string `json:"params"`
}

// OverlayLine is the config.txt line, "" for onboard audio (no overlay).
func (e Entry) OverlayLine() string {
	if e.ID == Onboard {
		return ""
	}
	if e.Params != "" {
		return "dtoverlay=" + e.ID + "," + e.Params
	}
	return "dtoverlay=" + e.ID
}

var Catalog = []Entry{
	{ID: Onboard, Label: "Onboard audio (3.5mm jack / HDMI)"},
	// Generic I2S
	{ID: "i2s-dac", Label: "Generic passive I2S DAC (Pi as clock master)"},
	{ID: "i2s-master-dac", Label: "Generic I2S DAC acting as clock master"},
	// HiFiBerry
	{ID: "hifiberry-dac", Label: "HiFiBerry DAC / DAC+ Light / DAC+ Zero / MiniAmp / PCM5102"},
	{ID: "hifiberry-dacplus", Label: "HiFiBerry DAC+ (auto-detect std/pro)"},
	{ID: "hifiberry-dacplus-std", Label: "HiFiBerry DAC+ (standard)"},
	{ID: "hifiberry-dacplus-pro", Label: "HiFiBerry DAC+ Pro / DAC2 Pro"},
	{ID: "hifiberry-dacplusadc", Label: "HiFiBerry DAC+ ADC"},
	{ID: "hifiberry-dacplusadcpro", Label: "HiFiBerry DAC+ ADC Pro / DAC2 ADC Pro"},
	{ID: "hifiberry-dacplushd", Label: "HiFiBerry DAC+ HD / DAC2 HD"},
	{ID: "hifiberry-dacplusdsp", Label: "HiFiBerry DAC+ DSP"},
	{ID: "hifiberry-dac8x", Label: "HiFiBerry DAC8x (Pi 5 only)"},
	{ID: "hifiberry-studio-dac8x", Label: "HiFiBerry Studio DAC8x"},
	{ID: "hifiberry-studio-dac8x-pro", Label: "HiFiBerry Studio DAC8x Pro"},
	{ID: "hifiberry-digi", Label: "HiFiBerry Digi / Digi+"},
	{ID: "hifiberry-digi-pro", Label: "HiFiBerry Digi+ Pro / Digi2 Pro"},
	{ID: "hifiberry-studio-digi", Label: "HiFiBerry Studio Digi / AES"},
	{ID: "hifiberry-amp", Label: "HiFiBerry Amp / Amp+"},
	{ID: "hifiberry-amp100", Label: "HiFiBerry Amp100"},
	{ID: "hifiberry-amp3", Label: "HiFiBerry Amp3"},
	{ID: "hifiberry-amp4pro", Label: "HiFiBerry Amp4 Pro"},
	// IQaudIO
	{ID: "iqaudio-dac", Label: "IQaudIO Pi-DAC / Pi-DAC Zero"},
	{ID: "iqaudio-dacplus", Label: "IQaudIO Pi-DAC+ / Pi-DAC Pro / Pi-DigiAMP+"},
	{ID: "iqaudio-digi-wm8804-audio", Label: "IQaudIO Pi-Digi+"},
	{ID: "iqaudio-codec", Label: "IQaudIO Pi-Codec+ / Codec Zero"},
	{ID: "akkordion-iqdacplus", Label: "Digital Dreamtime Akkordion (IQaudIO DAC+ based)"},
	// Raspberry Pi branded
	{ID: "rpi-dacplus", Label: "Raspberry Pi DAC+"},
	{ID: "rpi-dacpro", Label: "Raspberry Pi DAC Pro"},
	{ID: "rpi-digiampplus", Label: "Raspberry Pi DigiAMP+"},
	{ID: "rpi-codeczero", Label: "Raspberry Pi Codec Zero"},
	// Allo
	{ID: "allo-boss-dac-pcm512x-audio", Label: "Allo Boss DAC"},
	{ID: "allo-boss2-dac-audio", Label: "Allo Boss2 DAC"},
	{ID: "allo-piano-dac-pcm512x-audio", Label: "Allo Piano DAC 2.0 (2.1 in stereo only)"},
	{ID: "allo-piano-dac-plus-pcm512x-audio", Label: "Allo Piano DAC 2.1"},
	{ID: "allo-digione", Label: "Allo DigiOne"},
	{ID: "allo-katana-dac-audio", Label: "Allo Katana DAC"},
	// JustBoom
	{ID: "justboom-dac", Label: "JustBoom DAC HAT / Amp HAT / DAC Zero / Amp Zero"},
	{ID: "justboom-digi", Label: "JustBoom Digi HAT / Digi Zero"},
	{ID: "justboom-both", Label: "JustBoom DAC + Digi (stacked)"},
	// PiFi
	{ID: "pifi-dac-hd", Label: "PiFi DAC HD"},
	{ID: "pifi-dac-zero", Label: "PiFi DAC Zero"},
	{ID: "pifi-40", Label: "PiFi 40W stereo amplifier"},
	{ID: "pifi-mini-210", Label: "PiFi Mini stereo amplifier"},
	// Dion Audio
	{ID: "dionaudio-loco", Label: "Dion Audio LOCO DAC-AMP"},
	{ID: "dionaudio-loco-v2", Label: "Dion Audio LOCO-V2 DAC-AMP"},
	{ID: "dionaudio-kiwi", Label: "Dion Audio KIWI Streamer"},
	// AudioInjector
	{ID: "audioinjector-wm8731-audio", Label: "AudioInjector Zero / Stereo"},
	{ID: "audioinjector-addons", Label: "AudioInjector Octo"},
	{ID: "audioinjector-ultra", Label: "AudioInjector Ultra"},
	{ID: "audioinjector-isolated-soundcard", Label: "AudioInjector Isolated"},
	{ID: "audioinjector-bare-i2s", Label: "AudioInjector bare I2S"},
	// Blokas
	{ID: "pisound", Label: "Blokas Pisound"},
	{ID: "pisound-pi5", Label: "Blokas Pisound (Pi 5)"},
	{ID: "pisound-micro", Label: "Blokas Pisound Micro"},
	// Others
	{ID: "applepi-dac", Label: "Orchard Audio ApplePi-DAC"},
	{ID: "i-sabre-q2m", Label: "Audiophonics I-Sabre Q2M"},
	{ID: "rra-digidac1-wm8741-audio", Label: "Red Rocks Audio DigiDAC1"},
	{ID: "dacberry400", Label: "DACBerry 400"},
	{ID: "chipdip-dac", Label: "Chip Dip DAC"},
	{ID: "interludeaudio-analog", Label: "Interlude Audio Analog HAT"},
	{ID: "interludeaudio-digital", Label: "Interlude Audio Digital HAT"},
	{ID: "cirrus-wm5102", Label: "Cirrus Logic Audio Card"},
	{ID: "fe-pi-audio", Label: "Fe-Pi Audio"},
	{ID: "superaudioboard", Label: "SuperAudioBoard"},
	{ID: "proto-codec", Label: "PROTO Audio Codec (WM8731)"},
	{ID: "mbed-dac", Label: "mbed AudioCODEC (TLV320AIC23B)"},
	{ID: "max98357a", Label: "Maxim MAX98357A I2S amplifier"},
	{ID: "wm8960-soundcard", Label: "Waveshare WM8960 Audio HAT"},
	{ID: "googlevoicehat-soundcard", Label: "Google AIY Voice HAT"},
	{ID: "merus-amp", Label: "Infineon MERUS Audio Amp"},
	{ID: "ghost-amp", Label: "Ghost amplifier"},
	{ID: "audiosense-pi", Label: "AudioSense-Pi"},
	{ID: "pibell", Label: "PiBell"},
	{ID: "ezsound-6x8iso", Label: "ezsound-6x8 Pi5 multichannel soundcard"},
}

// ByID resolves a catalog entry.
func ByID(id string) (Entry, bool) {
	for _, e := range Catalog {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

var (
	overlayRE = regexp.MustCompile(`^\s*dtoverlay\s*=\s*([^,\s]+)(?:,(.*))?\s*$`)
	audioRE   = regexp.MustCompile(`(?i)^\s*dtparam\s*=\s*audio\s*=\s*(on|off)\s*$`)
)

// IsAudioOverlay reports whether the catalog knows this overlay. Anything
// else is left alone: guessing from a name is not a claim we can make.
func IsAudioOverlay(name string) bool {
	e, ok := ByID(name)
	return ok && e.ID != Onboard
}

func isAudioLine(line string) bool {
	if m := overlayRE.FindStringSubmatch(line); m != nil && IsAudioOverlay(m[1]) {
		return true
	}
	return audioRE.MatchString(line)
}

// splitBlock returns (before, block, after); block is nil when there is no
// managed block, and a Begin without End swallows to EOF.
func splitBlock(lines []string) (before, block, after []string) {
	start := slices.Index(lines, Begin)
	if start < 0 {
		return lines, nil, nil
	}
	end := slices.Index(lines[start+1:], End)
	if end < 0 {
		end = len(lines) - 1
	} else {
		end += start + 1
	}
	return lines[:start], lines[start : end+1], lines[end+1:]
}

func entryForLines(lines []string) string {
	audioOn := false
	for _, line := range lines {
		if m := overlayRE.FindStringSubmatch(line); m != nil && IsAudioOverlay(m[1]) {
			return m[1]
		}
		if a := audioRE.FindStringSubmatch(line); a != nil && strings.EqualFold(a[1], "on") {
			audioOn = true
		}
	}
	if audioOn {
		return Onboard
	}
	return ""
}

type ParsedConfig struct {
	Current    string   // entry ID, "" when nothing recognisable is configured
	Managed    bool     // the odioctl block is present
	StrayLines []string // active audio lines outside the managed block
}

func Parse(text string) ParsedConfig {
	before, block, after := splitBlock(strings.Split(text, "\n"))
	outside := slices.Concat(before, after)
	var stray []string
	for _, line := range outside {
		if isAudioLine(line) {
			stray = append(stray, line)
		}
	}
	if block != nil {
		return ParsedConfig{Current: entryForLines(block), Managed: true, StrayLines: stray}
	}
	return ParsedConfig{Current: entryForLines(outside), Managed: false, StrayLines: stray}
}

func renderBlock(e Entry) []string {
	audio := "off"
	if e.ID == Onboard {
		audio = "on"
	}
	lines := []string{Begin, "[all]", overlayReset, "dtparam=audio=" + audio}
	if dto := e.OverlayLine(); dto != "" {
		lines = append(lines, dto)
	}
	return append(lines, End)
}

func stripTrailingBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Apply returns config.txt text with e selected. Idempotent.
func Apply(text string, e Entry) string {
	before, _, after := splitBlock(strings.Split(text, "\n"))
	var body []string
	for _, line := range slices.Concat(before, after) {
		if isAudioLine(line) {
			body = append(body, DisabledPrefix+line)
		} else {
			body = append(body, line)
		}
	}
	body = stripTrailingBlank(body)
	if len(body) > 0 {
		body = append(body, "")
	}
	body = append(body, renderBlock(e)...)
	return strings.Join(body, "\n") + "\n"
}

// Unapply returns config.txt text with the managed block removed and disabled
// lines restored.
func Unapply(text string) string {
	before, _, after := splitBlock(strings.Split(text, "\n"))
	var body []string
	for _, line := range slices.Concat(before, after) {
		body = append(body, strings.TrimPrefix(line, DisabledPrefix))
	}
	body = stripTrailingBlank(body)
	if len(body) == 0 {
		return ""
	}
	return strings.Join(body, "\n") + "\n"
}

// FindConfigTxt returns the first existing config.txt candidate, "".
func FindConfigTxt() string {
	for _, p := range ConfigCandidates {
		if isFile(p) {
			return p
		}
	}
	return ""
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func ReadConfig(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// WriteConfig keeps a one-time .odioctl.bak of the pristine file, then writes
// atomically.
func WriteConfig(path, text string) error {
	backup := path + ".odioctl.bak"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if orig, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(backup, orig, 0o644); err != nil {
				slog.Warn("could not write backup", "path", backup, "err", err)
			}
		}
	}
	return fsutil.AtomicWriteText(path, text)
}

func MarkRebootRequired() {
	if err := os.MkdirAll(filepath.Dir(RebootFlag), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(RebootFlag, []byte("dac\n"), 0o644)
}

func RebootRequired() bool {
	_, err := os.Stat(RebootFlag)
	return err == nil
}

// Status is what the CLI and the web share about the current configuration.
type Status struct {
	Supported      bool     `json:"supported"`
	Config         string   `json:"config"`
	Current        string   `json:"current"`
	Managed        bool     `json:"managed"`
	StrayLines     []string `json:"stray_lines"`
	RebootRequired bool     `json:"reboot_required"`
}

// GetStatus inspects configPath ("" = auto-detect).
func GetStatus(configPath string) Status {
	path := configPath
	if path == "" {
		path = FindConfigTxt()
	}
	s := Status{Config: path, StrayLines: []string{}, RebootRequired: RebootRequired()}
	if path == "" {
		return s
	}
	text, err := ReadConfig(path)
	if err != nil {
		return s
	}
	p := Parse(text)
	s.Supported = true
	s.Current = p.Current
	s.Managed = p.Managed
	if p.StrayLines != nil {
		s.StrayLines = p.StrayLines
	}
	return s
}
