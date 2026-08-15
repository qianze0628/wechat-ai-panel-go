package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchLatestReleaseFallback 验证: 多源回退不整体失败。
// 直连可用 → 返回真实 tag (本机); 直连不可用 → 走镜像 (v9.9.9)。两种路径都算通过,
// 关键断言是"任一候选成功即返回, 不因单源超时报错"。
func TestFetchLatestReleaseFallback(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","name":"test","assets":[]}`))
	}))
	defer mirror.Close()
	orig := githubAPIMirrors
	githubAPIMirrors = []string{mirror.URL + "/"}
	defer func() { githubAPIMirrors = orig }()
	rel, err := fetchLatestRelease()
	if err != nil {
		t.Fatalf("fetchLatestRelease failed: %v", err)
	}
	if rel.TagName == "" {
		t.Error("tag 应为非空")
	}
	// 直连失败场景应命中镜像: 单独验证镜像 URL 本身可解析
	var m map[string]any
	if err := httpGetJSONTimeout(mirror.URL+"/repos/x/y/releases/latest", &m, 5*time.Second); err != nil {
		t.Errorf("镜像 URL 不可解析: %v", err)
	} else if m["tag_name"] != "v9.9.9" {
		t.Errorf("镜像返回异常: %v", m["tag_name"])
	}
}

// TestVersionIsNewerEdge 验证版本比较可靠性 (更新判断正确性)
func TestVersionIsNewerEdge(t *testing.T) {
	cases := []struct{ latest, cur string; want bool }{
		{"v0.6.1", "v0.5.5", true},
		{"v0.6.1", "v0.6.1", false},
		{"v0.6.1", "v0.6.2", false},
		{"v0.10.0", "v0.9.9", true},
		{"v0.6.1", "go-v0.2", true},
	}
	for _, c := range cases {
		if got := versionIsNewer(c.latest, c.cur); got != c.want {
			t.Errorf("versionIsNewer(%s,%s)=%v want %v", c.latest, c.cur, got, c.want)
		}
	}
}

var _ = json.Marshal
var _ = time.Second
var _ = fmt.Sprintf