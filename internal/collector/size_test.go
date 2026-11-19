package collector

import "testing"

func TestFormatSizeDecimal(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "  unknown"},
		{0, "    0 B"},
		{999, "  999 B"},
		{1000, "  1.0 KB"},
		{1_500_000, "  1.5 MB"},
		{8_700_000_000, "  8.7 GB"},
		{243_200_000_000, "243.2 GB"},
		{1_200_000_000_000, "  1.2 TB"},
	}
	for _, c := range cases {
		if got := FormatSize(c.in); got != c.want {
			t.Errorf("FormatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

const releaseSizeOutput = `Name:           4.22.21
OS/Arch:        multi (linux/amd64)

Images:
  NAME                                            AGE  LAYERS  SIZE MB  UNIQUE MB  BASE
  agent-installer-api-server                      22d       4    398.4      327.6  A
  tools                                           22d       4    231.7      160.9  A

  21.99GB across 333 layers, 4 different base images
`

func TestParseReleaseSize(t *testing.T) {
	// 末尾の合計行 (共有レイヤーを重複なく数えた値、1000 単位) を使う
	got, err := parseReleaseSize(releaseSizeOutput)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(21_990_000_000); got != want {
		t.Errorf("got %d, want %d", got, want)
	}

	// 合計行が無い場合はコンポーネントの SIZE MB を足す
	noTotal := `Images:
  NAME                AGE  LAYERS  SIZE MB  UNIQUE MB  BASE
  a                   22d       4    100.0       10.0  A
  b                   22d       4     50.0        5.0  A
`
	got, err = parseReleaseSize(noTotal)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(150 * 1024 * 1024); got != want {
		t.Errorf("fallback: got %d, want %d", got, want)
	}

	if _, err := parseReleaseSize("error: no matching image found in manifest list\n"); err == nil {
		t.Error("expected error for output without size information")
	}
}

func TestMostCommon(t *testing.T) {
	if got := mostCommon(map[string]int{"amd64": 3, "arm64": 1}, "x"); got != "amd64" {
		t.Errorf("got %q", got)
	}
	if got := mostCommon(map[string]int{"arm64": 2, "amd64": 2}, "x"); got != "amd64" {
		t.Errorf("tie should pick the name sorted first, got %q", got)
	}
	if got := mostCommon(map[string]int{}, "amd64"); got != "amd64" {
		t.Errorf("empty should return default, got %q", got)
	}
}
