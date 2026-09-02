// Command gen writes data/sudoers/odioctl from dac.SudoersFragment.
// Run from the dac package dir (what `go generate ./dac` does); -check only
// reports drift.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/b0bbywan/odioctl/dac"
)

func main() {
	check := flag.Bool("check", false, "fail on drift instead of writing")
	flag.Parse()
	const out = "../data/sudoers/odioctl"
	text := dac.SudoersFragment()
	if *check {
		current, _ := os.ReadFile(out)
		if string(current) != text {
			fmt.Fprintf(os.Stderr, "%s is out of date — run go generate ./dac\n", out)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}
