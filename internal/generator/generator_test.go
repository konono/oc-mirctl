package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func writeData(t *testing.T, dir, file string, d CollectedData) {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func generate(t *testing.T, dir string, opts Options) ImageSetConfig {
	t.Helper()
	g, err := New(dir, "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.GenerateImageSetConfig(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "imageset-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg ImageSetConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

var testOp = OperatorInfo{
	PackageName:   "rhods-operator",
	Channel:       "stable",
	Version:       "rhods-operator.3.5.1",
	CatalogSource: "redhat-operators",
}

func TestPinOperators(t *testing.T) {
	cases := []struct {
		pin     bool
		wantMax string
	}{
		{pin: false, wantMax: ""},
		{pin: true, wantMax: "3.5.1"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.20.1", Operators: []OperatorInfo{testOp}})
		cfg := generate(t, dir, Options{DataFile: "current-data.json", PinOperators: c.pin})
		ch := cfg.Mirror.Operators[0].Packages[0].Channels[0]
		if ch.MinVersion != "3.5.1" || ch.MaxVersion != c.wantMax {
			t.Errorf("pin=%v: got min=%q max=%q, want min=3.5.1 max=%q", c.pin, ch.MinVersion, ch.MaxVersion, c.wantMax)
		}
	}
}

func TestDataFileSelection(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "current-data.json", CollectedData{
		OCPVersion:   "4.20.1",
		Operators:    []OperatorInfo{testOp},
		CustomImages: []string{"example.com/app@sha256:new"},
	})
	writeData(t, dir, "collected-data.json", CollectedData{
		OCPVersion:   "4.20.1",
		Operators:    []OperatorInfo{testOp},
		CustomImages: []string{"example.com/app@sha256:new", "example.com/app@sha256:old"},
	})

	cur := generate(t, dir, Options{DataFile: "current-data.json"})
	if n := len(cur.Mirror.AdditionalImages); n != 1 || cur.Mirror.AdditionalImages[0].Name != "example.com/app@sha256:new" {
		t.Errorf("current: got %v, want only sha256:new", cur.Mirror.AdditionalImages)
	}
	all := generate(t, dir, Options{DataFile: "collected-data.json"})
	if n := len(all.Mirror.AdditionalImages); n != 2 {
		t.Errorf("all: got %d additionalImages, want 2", n)
	}
}
