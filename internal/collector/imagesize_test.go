package collector

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReportSizeFetchFailures_NoFailures(t *testing.T) {
	var buf bytes.Buffer
	reportSizeFetchFailures(&buf, nil, 10)
	if buf.Len() != 0 {
		t.Fatalf("失敗 0 件なら何も出力しないはず: %q", buf.String())
	}
}

func TestReportSizeFetchFailures_Partial(t *testing.T) {
	var buf bytes.Buffer
	failures := []sizeFetchFailure{
		{Image: "quay.io/b/img@sha256:2", Err: errors.New("skopeo inspect failed: exit status 1: unauthorized")},
		{Image: "quay.io/a/img@sha256:1", Err: errors.New("skopeo inspect failed: exit status 1: proxyconnect tcp: dial tcp 10.0.0.1:3128: i/o timeout\n")},
	}
	reportSizeFetchFailures(&buf, failures, 10)
	out := buf.String()

	for _, want := range []string{"2/10 イメージのサイズ取得に失敗", "unauthorized", "proxyconnect tcp"} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が含まれない:\n%s", want, out)
		}
	}
	if strings.Contains(out, "全イメージのサイズ取得に失敗") {
		t.Errorf("一部失敗では全滅警告を出さないはず:\n%s", out)
	}
	// イメージ名順に並ぶこと
	if strings.Index(out, "quay.io/a/") > strings.Index(out, "quay.io/b/") {
		t.Errorf("イメージ名順に表示されていない:\n%s", out)
	}
}

func TestReportSizeFetchFailures_AllFailedTruncatesList(t *testing.T) {
	var buf bytes.Buffer
	var failures []sizeFetchFailure
	for i := 0; i < 8; i++ {
		failures = append(failures, sizeFetchFailure{
			Image: fmt.Sprintf("registry.example.com/img%d:latest", i),
			Err:   errors.New("connection refused"),
		})
	}
	reportSizeFetchFailures(&buf, failures, 8)
	out := buf.String()

	if !strings.Contains(out, "全イメージのサイズ取得に失敗") {
		t.Errorf("全滅時は警告を出すはず:\n%s", out)
	}
	if !strings.Contains(out, "NO_PROXY") {
		t.Errorf("全滅警告にプロキシのヒントが無い:\n%s", out)
	}
	if !strings.Contains(out, "他 3 件") {
		t.Errorf("5 件を超えた分は省略表示されるはず:\n%s", out)
	}
	if strings.Contains(out, "img5:") {
		t.Errorf("6 件目以降は詳細表示しないはず:\n%s", out)
	}
}

func TestSummarizeError(t *testing.T) {
	got := summarizeError(errors.New("line1\n  line2\t\tend\n"))
	if got != "line1 line2 end" {
		t.Errorf("改行・空白を 1 行にまとめるはず: %q", got)
	}

	long := summarizeError(errors.New(strings.Repeat("あ", maxErrorLineLen+10) + " proxyconnect tcp: refused"))
	if !strings.HasPrefix(long, "...") || len([]rune(long)) != maxErrorLineLen+3 {
		t.Errorf("長いエラーは rune 単位で先頭を切り詰めるはず: len=%d", len([]rune(long)))
	}
	if !strings.HasSuffix(long, "proxyconnect tcp: refused") {
		t.Errorf("末尾の根本原因は残すはず: %q", long)
	}
}
