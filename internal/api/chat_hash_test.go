package api

import "testing"

// TestHashNameOfGoJSCompatible: Go hashNameOf 与 wechat-bot bridge-integration.js hashId 完全一致
// JS: ((h<<5)-h+charCode)|0 每字符 → Math.abs(h)+10000
// 参考值由 node 计算 (2026-08-11): 罗钰博 32538961 (正是被白名单拦截的联系人)
func TestHashNameOfGoJSCompatible(t *testing.T) {
	cases := map[string]string{
		"罗钰博":       "32538961",
		"秦晓洁":       "30838916",
		"谴责":        "1158783",
		"丁玉恒":       "20141754",
		"徐邵博":       "24689637",
		"电脑开黑":      "926435933",
		"A.":        "12061",
		"微信用户":      "750317138",
		"未知名成员":     "443771350",
	}
	for name, want := range cases {
		if got := hashNameOf(name); got != want {
			t.Errorf("hashNameOf(%q) = %s, want %s", name, got, want)
		}
	}
}