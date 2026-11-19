package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestDefaultChannelHandling(t *testing.T) {
	op := OperatorInfo{
		PackageName:    "rhods-operator",
		Channel:        "stable-3.5",
		DefaultChannel: "stable-3.x",
		Version:        "rhods-operator.3.5.3",
		CatalogSource:  "redhat-operators",
	}

	// デフォルト: defaultChannel をチャネル名のみで追加 (head をミラー)
	dir := t.TempDir()
	writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	pkg := generate(t, dir, Options{DataFile: "current-data.json"}).Mirror.Operators[0].Packages[0]
	if pkg.DefaultChannel != "" || len(pkg.Channels) != 2 || pkg.Channels[1].Name != "stable-3.x" || pkg.Channels[1].MinVersion != "" {
		t.Errorf("default: got %+v, want stable-3.x appended without range and no defaultChannel override", pkg)
	}

	// --pin-operators: defaultChannel を Subscription のチャネルで上書きし、stable-3.x は含めない
	pkg = generate(t, dir, Options{DataFile: "current-data.json", PinOperators: true}).Mirror.Operators[0].Packages[0]
	if pkg.DefaultChannel != "stable-3.5" || len(pkg.Channels) != 1 || pkg.Channels[0].Name != "stable-3.5" {
		t.Errorf("pin: got %+v, want defaultChannel=stable-3.5 and only stable-3.5", pkg)
	}

	// チャネルが defaultChannel と同じなら上書きもしない
	op.Channel = "stable-3.x"
	writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	pkg = generate(t, dir, Options{DataFile: "current-data.json", PinOperators: true}).Mirror.Operators[0].Packages[0]
	if pkg.DefaultChannel != "" || len(pkg.Channels) != 1 {
		t.Errorf("pin same channel: got %+v, want no override and single channel", pkg)
	}
}

func TestBundleVersionPreferred(t *testing.T) {
	op := OperatorInfo{
		PackageName:   "gpu-operator-certified",
		Channel:       "v26.7",
		Version:       "gpu-operator-certified.v26.7.2",
		BundleVersion: "26.7.2",
		CatalogSource: "certified-operators",
	}
	dir := t.TempDir()
	writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	ch := generate(t, dir, Options{DataFile: "current-data.json", PinOperators: true}).Mirror.Operators[0].Packages[0].Channels[0]
	if ch.MinVersion != "26.7.2" || ch.MaxVersion != "26.7.2" {
		t.Errorf("got min=%q max=%q, want 26.7.2 from spec.version", ch.MinVersion, ch.MaxVersion)
	}

	// 旧データ (bundleVersion なし) は CSV 名から推定
	op.BundleVersion = ""
	writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	ch = generate(t, dir, Options{DataFile: "current-data.json"}).Mirror.Operators[0].Packages[0].Channels[0]
	if ch.MinVersion != "v26.7.2" {
		t.Errorf("fallback: got min=%q, want v26.7.2", ch.MinVersion)
	}
}

func TestCatalogFallback(t *testing.T) {
	ops := []OperatorInfo{
		{PackageName: "nfd", Channel: "stable", CatalogSource: "redhat-operators"},
		{PackageName: "gpu-operator-certified", Channel: "v26.7", CatalogSource: "certified-operators"},
		{PackageName: "dvo", Channel: "alpha", CatalogSource: "custom-catalog"},
	}
	cats := []CatalogSource{
		{Name: "redhat-operators"},
		{Name: "certified-operators"},
		{Name: "custom-catalog", Image: "quay.io/example/catalog@sha256:abc"},
	}
	dir := t.TempDir()
	writeData(t, dir, "current-data.json", CollectedData{OCPVersion: "4.22.21", Operators: ops, CatalogSources: cats})
	cfg := generate(t, dir, Options{DataFile: "current-data.json"})

	got := map[string]string{}
	for _, c := range cfg.Mirror.Operators {
		for _, p := range c.Packages {
			got[p.Name] = c.Catalog
		}
	}
	want := map[string]string{
		"nfd":                    "registry.redhat.io/redhat/redhat-operator-index:v4.22",
		"gpu-operator-certified": "registry.redhat.io/redhat/certified-operator-index:v4.22",
		"dvo":                    "quay.io/example/catalog@sha256:abc",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got catalog %q, want %q", k, got[k], v)
		}
	}
	if len(cfg.Mirror.Operators) != 3 {
		t.Errorf("got %d catalogs, want 3", len(cfg.Mirror.Operators))
	}

	// 不明なカタログで image が空ならエラー
	writeData(t, dir, "current-data.json", CollectedData{
		OCPVersion:     "4.22.21",
		Operators:      []OperatorInfo{{PackageName: "x", Channel: "a", CatalogSource: "my-catalog"}},
		CatalogSources: []CatalogSource{{Name: "my-catalog"}},
	})
	g, err := New(dir, "", Options{DataFile: "current-data.json"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.GenerateImageSetConfig(); err == nil || !strings.Contains(err.Error(), "my-catalog") {
		t.Errorf("expected error for unknown catalog without image, got %v", err)
	}
}

func TestUntaggedAdditionalImages(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "collected-data.json", CollectedData{
		OCPVersion: "4.22.21",
		CustomImages: []string{
			"registry.access.redhat.com/ubi9/ubi-minimal",
			"registry.local:5000/app",
			"quay.io/x/app:v1",
			"quay.io/x/app@sha256:aaa",
		},
	})
	cfg := generate(t, dir, Options{DataFile: "collected-data.json"})
	got := map[string]bool{}
	for _, img := range cfg.Mirror.AdditionalImages {
		got[img.Name] = true
	}
	for _, want := range []string{
		"registry.access.redhat.com/ubi9/ubi-minimal:latest",
		"registry.local:5000/app:latest",
		"quay.io/x/app:v1",
		"quay.io/x/app@sha256:aaa",
	} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, cfg.Mirror.AdditionalImages)
		}
	}
}
