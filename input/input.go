// Package input manages the remote controls' overlays in the Raspberry Pi
// config.txt: the infrared receiver's `dtoverlay=gpio-ir`, the only one so
// far.
//
// odioctl owns one marked block, last in config.txt:
//
//	# BEGIN odioctl input -- managed block, edit with `odioctl input`
//	[all]
//	dtoverlay=gpio-ir,gpio_pin=25
//	# END odioctl input
//
// Last, so nothing of the file falls in an overlay's scope; [all] closes
// whatever filter section the file ends in. The pin is always written: the
// overlay's own default, 18, is I2S's bit clock, which the DACs hold. A
// gpio-ir line of the file's is commented out with DisabledPrefix so
// `input ir unset` restores it. Writing needs root.
package input

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/b0bbywan/odioctl/configtxt"
)

const (
	Begin          = "# BEGIN odioctl input -- managed block, edit with `odioctl input`"
	End            = "# END odioctl input"
	DisabledPrefix = "#odioctl-input-disabled: "

	IROverlay = "gpio-ir"

	// irOverlayPin is where the overlay listens when a line names no pin.
	irOverlayPin = 18
)

var block = configtxt.Block{Begin: Begin, End: End, Disabled: DisabledPrefix}

// Pins are the GPIOs (BCM numbers) an input can be set on: the header's,
// less I2C (2, 3) and I2S (18 to 21), which the DACs hold.
var Pins = []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 22, 23, 24, 25, 26, 27}

// ParsePin reads a pin of Pins.
func ParsePin(s string) (int, bool) {
	pin, err := strconv.Atoi(s)
	return pin, err == nil && slices.Contains(Pins, pin)
}

// irPinOf is the pin a gpio-ir line listens on; false for any other line.
func irPinOf(line string) (int, bool) {
	name, params, ok := configtxt.Overlay(line)
	if !ok || name != IROverlay {
		return 0, false
	}
	pin := irOverlayPin
	for _, param := range strings.Split(params, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(param), "gpio_pin="); ok {
			if n, err := strconv.ParseInt(v, 0, 0); err == nil {
				pin = int(n)
			}
		}
	}
	return pin, true
}

func isIRLine(line string) bool {
	_, ok := irPinOf(line)
	return ok
}

type ParsedConfig struct {
	IRPin      int      // 0 when no receiver is configured
	Managed    bool     // the odioctl block is present
	StrayLines []string // active gpio-ir lines outside the managed block
}

func Parse(text string) ParsedConfig {
	before, managed, after := block.Split(strings.Split(text, "\n"))
	p := ParsedConfig{Managed: managed != nil}
	for _, line := range slices.Concat(before, after) {
		if pin, ok := irPinOf(line); ok {
			if len(p.StrayLines) == 0 {
				p.IRPin = pin
			}
			p.StrayLines = append(p.StrayLines, line)
		}
	}
	if p.Managed {
		p.IRPin = 0 // the block wins over the file's own lines
		for _, line := range managed {
			if pin, ok := irPinOf(line); ok {
				p.IRPin = pin
			}
		}
	}
	return p
}

// ApplyIR returns config.txt text with the receiver on pin. Idempotent.
func ApplyIR(text string, pin int) string {
	lines := block.Rest(text, isIRLine)
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, Begin, "[all]", fmt.Sprintf("dtoverlay=%s,gpio_pin=%d", IROverlay, pin), End)
	return strings.Join(lines, "\n") + "\n"
}

// UnapplyIR returns config.txt text without the receiver: the managed block
// removed, which holds nothing else, and the disabled lines restored.
func UnapplyIR(text string) string {
	return block.Remove(text)
}

// WouldChangeIR reports whether setting the receiver on pin would rewrite path.
func WouldChangeIR(path string, pin int) (bool, error) {
	text, err := configtxt.Read(configtxt.Resolve(path))
	if err != nil {
		return false, err
	}
	return ApplyIR(text, pin) != text, nil
}

// Status is what the CLI and the web share about the current configuration.
type Status struct {
	Supported      bool     `json:"supported"`
	Config         string   `json:"config"`
	IRPin          int      `json:"ir_pin"` // 0 when no receiver is configured
	Managed        bool     `json:"managed"`
	StrayLines     []string `json:"stray_lines"`
	RebootRequired bool     `json:"reboot_required"`
}

// GetStatus inspects configPath ("" = configtxt.Path). A missing file reads
// as Supported=false: not a Raspberry Pi.
func GetStatus(configPath string) Status {
	path := configtxt.Resolve(configPath)
	s := Status{Config: path, StrayLines: []string{}, RebootRequired: configtxt.RebootRequired()}
	text, err := configtxt.Read(path)
	if err != nil {
		return s
	}
	p := Parse(text)
	s.Supported = true
	s.IRPin = p.IRPin
	s.Managed = p.Managed
	if p.StrayLines != nil {
		s.StrayLines = p.StrayLines
	}
	return s
}
