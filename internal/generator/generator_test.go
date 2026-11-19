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
		writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.20.1", Operators: []OperatorInfo{testOp}})
		cfg := generate(t, dir, Options{PinOperators: c.pin})
		ch := cfg.Mirror.Operators[0].Packages[0].Channels[0]
		if ch.MinVersion != "3.5.1" || ch.MaxVersion != c.wantMax {
			t.Errorf("pin=%v: got min=%q max=%q, want min=3.5.1 max=%q", c.pin, ch.MinVersion, ch.MaxVersion, c.wantMax)
		}
	}
}

func TestAllModeIncludesUnusedImages(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "collected-data.json", CollectedData{
		OCPVersion:   "4.20.1",
		Operators:    []OperatorInfo{testOp},
		CustomImages: []string{"example.com/app@sha256:new"},
		UnusedImages: []string{"example.com/app@sha256:old"},
	})

	cur := generate(t, dir, Options{})
	if n := len(cur.Mirror.AdditionalImages); n != 1 || cur.Mirror.AdditionalImages[0].Name != "example.com/app@sha256:new" {
		t.Errorf("default: got %v, want only sha256:new", cur.Mirror.AdditionalImages)
	}
	all := generate(t, dir, Options{All: true})
	if n := len(all.Mirror.AdditionalImages); n != 2 {
		t.Errorf("all: got %d additionalImages, want 2", n)
	}
}

func TestAllModeOperatorPastVersions(t *testing.T) {
	op := OperatorInfo{
		PackageName:   "gpu-operator-certified",
		Channel:       "v26.7",
		Version:       "gpu-operator-certified.v26.7.3",
		BundleVersion: "26.7.3",
		CatalogSource: "certified-operators",
		PastVersions: []PastVersion{
			{CSV: "gpu-operator-certified.v26.3.1", Version: "26.3.1", Channel: "v26.3"},
			{CSV: "gpu-operator-certified.v26.3.3", Version: "26.3.3", Channel: "v26.3"},
			{CSV: "gpu-operator-certified.v26.7.2", Version: "26.7.2", Channel: "v26.7"},
			{CSV: "gpu-operator-certified.v25.0.0", Version: "25.0.0", Channel: ""},
		},
	}
	dir := t.TempDir()
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})

	// デフォルト: 過去の版は無視
	chs := generate(t, dir, Options{PinOperators: true}).Mirror.Operators[0].Packages[0].Channels
	if len(chs) != 1 || chs[0].MinVersion != "26.7.3" || chs[0].MaxVersion != "26.7.3" {
		t.Errorf("default: got %+v", chs)
	}

	// --all --pin-operators: 現チャネルは 26.7.2〜26.7.3、v26.3 は 26.3.1〜26.3.3、カタログに無い版は除外
	chs = generate(t, dir, Options{All: true, PinOperators: true}).Mirror.Operators[0].Packages[0].Channels
	want := []PkgChannelRef{
		{Name: "v26.7", MinVersion: "26.7.2", MaxVersion: "26.7.3"},
		{Name: "v26.3", MinVersion: "26.3.1", MaxVersion: "26.3.3"},
	}
	if len(chs) != len(want) {
		t.Fatalf("all+pin: got %+v, want %+v", chs, want)
	}
	for i := range want {
		if chs[i] != want[i] {
			t.Errorf("all+pin [%d]: got %+v, want %+v", i, chs[i], want[i])
		}
	}

	// --all (pin なし): 現チャネルは maxVersion なし
	chs = generate(t, dir, Options{All: true}).Mirror.Operators[0].Packages[0].Channels
	if chs[0].MinVersion != "26.7.2" || chs[0].MaxVersion != "" {
		t.Errorf("all: got %+v", chs[0])
	}
}

func TestAllModePlatformHistory(t *testing.T) {
	dir := t.TempDir()
	writeData(t, dir, "collected-data.json", CollectedData{
		OCPVersion: "4.22.21",
		OCPChannel: "stable-4.22",
		PastReleases: []ReleaseEntry{
			{Version: "4.20.5"}, {Version: "4.20.12"}, {Version: "4.21.3"}, {Version: "4.22.10"},
		},
	})
	plat := generate(t, dir, Options{}).Mirror.Platform.Channels
	if len(plat) != 1 || plat[0].MinVersion != "4.22.21" {
		t.Errorf("default: got %+v", plat)
	}
	plat = generate(t, dir, Options{All: true}).Mirror.Platform.Channels
	want := []ChannelConfig{
		{Name: "stable-4.20", MinVersion: "4.20.5", MaxVersion: "4.20.12"},
		{Name: "stable-4.21", MinVersion: "4.21.3", MaxVersion: "4.21.3"},
		{Name: "stable-4.22", MinVersion: "4.22.10", MaxVersion: "4.22.21"},
	}
	if len(plat) != len(want) {
		t.Fatalf("all: got %+v, want %+v", plat, want)
	}
	for i := range want {
		if plat[i] != want[i] {
			t.Errorf("all [%d]: got %+v, want %+v", i, plat[i], want[i])
		}
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
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	pkg := generate(t, dir, Options{}).Mirror.Operators[0].Packages[0]
	if pkg.DefaultChannel != "" || len(pkg.Channels) != 2 || pkg.Channels[1].Name != "stable-3.x" || pkg.Channels[1].MinVersion != "" {
		t.Errorf("default: got %+v, want stable-3.x appended without range and no defaultChannel override", pkg)
	}

	// --pin-operators: defaultChannel を Subscription のチャネルで上書きし、stable-3.x は含めない
	pkg = generate(t, dir, Options{PinOperators: true}).Mirror.Operators[0].Packages[0]
	if pkg.DefaultChannel != "stable-3.5" || len(pkg.Channels) != 1 || pkg.Channels[0].Name != "stable-3.5" {
		t.Errorf("pin: got %+v, want defaultChannel=stable-3.5 and only stable-3.5", pkg)
	}

	// チャネルが defaultChannel と同じなら上書きもしない
	op.Channel = "stable-3.x"
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	pkg = generate(t, dir, Options{PinOperators: true}).Mirror.Operators[0].Packages[0]
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
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	ch := generate(t, dir, Options{PinOperators: true}).Mirror.Operators[0].Packages[0].Channels[0]
	if ch.MinVersion != "26.7.2" || ch.MaxVersion != "26.7.2" {
		t.Errorf("got min=%q max=%q, want 26.7.2 from spec.version", ch.MinVersion, ch.MaxVersion)
	}

	// 旧データ (bundleVersion なし) は CSV 名から推定
	op.BundleVersion = ""
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})
	ch = generate(t, dir, Options{}).Mirror.Operators[0].Packages[0].Channels[0]
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
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: ops, CatalogSources: cats})
	cfg := generate(t, dir, Options{})

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
	writeData(t, dir, "collected-data.json", CollectedData{
		OCPVersion:     "4.22.21",
		Operators:      []OperatorInfo{{PackageName: "x", Channel: "a", CatalogSource: "my-catalog"}},
		CatalogSources: []CatalogSource{{Name: "my-catalog"}},
	})
	g, err := New(dir, "", Options{})
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
	cfg := generate(t, dir, Options{})
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

// 版がインストール順に単調増加しない履歴 (ダウングレードを含む) でも
// minVersion <= maxVersion になること
func TestAllModeNonMonotonicHistory(t *testing.T) {
	op := OperatorInfo{
		PackageName:   "example-operator",
		Channel:       "stable",
		Version:       "example-operator.v1.5.0",
		BundleVersion: "1.5.0",
		CatalogSource: "redhat-operators",
		PastVersions: []PastVersion{
			// インストール順: 2.0.0 → 1.2.0 → (現行) 1.5.0
			{Version: "2.0.0", Channel: "stable"},
			{Version: "1.2.0", Channel: "stable"},
			// 別チャネルもインストール順と版の順が逆
			{Version: "3.1.0", Channel: "beta"},
			{Version: "3.0.0", Channel: "beta"},
		},
	}
	dir := t.TempDir()
	writeData(t, dir, "collected-data.json", CollectedData{OCPVersion: "4.22.21", Operators: []OperatorInfo{op}})

	chs := generate(t, dir, Options{All: true, PinOperators: true}).Mirror.Operators[0].Packages[0].Channels
	want := []PkgChannelRef{
		{Name: "stable", MinVersion: "1.2.0", MaxVersion: "2.0.0"},
		{Name: "beta", MinVersion: "3.0.0", MaxVersion: "3.1.0"},
	}
	if len(chs) != len(want) {
		t.Fatalf("got %+v, want %+v", chs, want)
	}
	for i := range want {
		if chs[i] != want[i] {
			t.Errorf("[%d] got %+v, want %+v", i, chs[i], want[i])
		}
		if compareVersions(chs[i].MinVersion, chs[i].MaxVersion) > 0 {
			t.Errorf("[%d] minVersion %s > maxVersion %s", i, chs[i].MinVersion, chs[i].MaxVersion)
		}
	}

	// pin なしでも現チャネルの下限は最も低い版
	chs = generate(t, dir, Options{All: true}).Mirror.Operators[0].Packages[0].Channels
	if chs[0].MinVersion != "1.2.0" || chs[0].MaxVersion != "" {
		t.Errorf("all without pin: got %+v", chs[0])
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"4.22.21", "4.22.9", 1},
		{"4.9.0", "4.10.0", -1},
		{"1.5.0", "1.5.0", 0},
		{"v26.7.2", "26.7.3", -1},
		// プレリリース (nfd の日付付き版など)
		{"4.22.0-202611040403", "4.22.0-202609151747", 1},
		{"4.22.0-202611040403", "4.22.0", -1},
		{"3.6.0-ea.2", "3.6.0-ea.10", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-beta", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0.0+build.5", "1.0.0", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := compareVersions(c.b, c.a); got != -c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}
