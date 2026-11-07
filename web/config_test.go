package web

import (
	"os"
	"testing"
)

func TestDefaultConfigOdioAPI(t *testing.T) {
	t.Setenv(OdioAPIEnv, "") // restored on cleanup
	if got := DefaultConfig().OdioAPI; got != "" {
		t.Errorf("set and empty: OdioAPI = %q, want none", got)
	}
	t.Setenv(OdioAPIEnv, "http://127.0.0.1:9018")
	if got := DefaultConfig().OdioAPI; got != "http://127.0.0.1:9018" {
		t.Errorf("set: OdioAPI = %q", got)
	}
	if err := os.Unsetenv(OdioAPIEnv); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig().OdioAPI; got != "http://127.0.0.1:8018" {
		t.Errorf("unset: OdioAPI = %q, want odio-api on lo", got)
	}
}

func TestOdioAPIBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "",
		"http://127.0.0.1:8018":   "http://127.0.0.1:8018",
		"http://127.0.0.1:8018/":  "http://127.0.0.1:8018",
		"http://127.0.0.1:8018//": "http://127.0.0.1:8018",
		"https://odio.local":      "https://odio.local",
	} {
		if got, err := odioAPIBase(in); err != nil || got != want {
			t.Errorf("odioAPIBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"127.0.0.1:8018", // no scheme: a parse error
		"localhost:8018", // no scheme: localhost is one
		"ftp://x", "http://", "http://h:1/api", "http://h?x=1", "http://u@h",
	} {
		if got, err := odioAPIBase(in); err == nil {
			t.Errorf("odioAPIBase(%q) = %q, want refused", in, got)
		}
	}
}
