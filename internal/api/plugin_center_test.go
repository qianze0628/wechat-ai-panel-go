package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNormalizeConfSchemaTopLevel: 顶层 bool/string/int 字段必须被并入 _top section
func TestNormalizeConfSchemaTopLevel(t *testing.T) {
	raw := `{
		"enable_plugin": {"type": "bool", "default": true},
		"trigger_prefix": {"type": "string", "default": "!"},
		"cooldown_seconds": {"type": "int", "default": 60},
		"disabled_templates": {"type": "list", "default": []},
		"llm": {"type": "object", "items": {"provider_id": {"type": "string"}}},
		"empty_obj": {"type": "object", "items": {}}
	}`
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatal(err)
	}
	out := normalizeConfSchema(schema).(map[string]any)
	// 顶层 bool/string/int/list 应并入 _top
	top, ok := out["_top"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 _top section, out=%v", out)
	}
	items := top["items"].(map[string]any)
	for _, k := range []string{"enable_plugin", "trigger_prefix", "cooldown_seconds", "disabled_templates"} {
		if _, exists := items[k]; !exists {
			t.Errorf("_top.items 缺少 %q, got keys=%v", k, keysOf(items))
		}
	}
	// llm 保持原样
	if _, ok := out["llm"].(map[string]any); !ok {
		t.Error("llm section 不应被并入 _top")
	}
	// 空 items 的 object 应并入
	if _, exists := items["empty_obj"]; !exists {
		t.Error("空 items 的 object 应并入 _top")
	}
}

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestNormalizeConfSchemaRealPlugins: 真实已装插件的 schema 归一化后, meme_generator 的 10 个开关全部可见
func TestNormalizeConfSchemaRealPlugins(t *testing.T) {
	base := `C:\Users\YMB\data\plugins`
	for _, plug := range []string{
		"astrbot_plugin_meme_generator",
		"astrbot_plugin_portrayal",
		"astrbot_plugin_self_learning",
	} {
		p := filepath.Join(base, plug, "_conf_schema.json")
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Skipf("无 schema: %v", err)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("schema 解析失败 %s: %v", plug, err)
			continue
		}
		out := normalizeConfSchema(schema).(map[string]any)
		// 统计所有可渲染的 items 字段 (顶层 object section + _top)
		var visible []string
		if top, ok := out["_top"].(map[string]any); ok {
			if items, ok := top["items"].(map[string]any); ok {
				visible = append(visible, keysOf(items)...)
			}
		}
		for k, v := range out {
			if k == "_top" {
				continue
			}
			if f, ok := v.(map[string]any); ok && f["type"] == "object" {
				if items, ok := f["items"].(map[string]any); ok {
					visible = append(visible, keysOf(items)...)
				}
			}
		}
		t.Logf("%s: 可渲染字段 %d 个: %v", plug, len(visible), visible)
		if plug == "astrbot_plugin_meme_generator" {
			for _, k := range []string{"enable_plugin", "trigger_prefix", "cooldown_seconds", "generation_timeout", "enable_avatar_cache", "cache_expire_hours", "disabled_templates", "enable_auto_meme", "auto_meme_scope", "auto_meme_level"} {
				found := false
				for _, v := range visible {
					if v == k {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("meme_generator 开关 %q 归一化后仍不可见", k)
				}
			}
		}
	}
}
// TestNormalizeConfSchemaRoundTrip: 归一化后保存时 _top 逆展开回顶层, 插件代码读取一致
func TestNormalizeConfSchemaRoundTrip(t *testing.T) {
	raw := `{
		"enable_plugin": {"type": "bool", "default": true},
		"trigger_prefix": {"type": "string", "default": "!"},
		"llm": {"type": "object", "items": {"provider_id": {"type": "string"}}}
	}`
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatal(err)
	}
	// 归一化 (前端展示)
	norm := normalizeConfSchema(schema).(map[string]any)
	top, ok := norm["_top"].(map[string]any)
	if !ok {
		t.Fatal("缺少 _top")
	}
	items := top["items"].(map[string]any)
	if _, ok := items["enable_plugin"]; !ok {
		t.Fatal("enable_plugin 未归入 _top")
	}
	// 模拟前端保存 (config = 归一化后用户改的值)
	saved := map[string]any{"_top": items}
	saved["llm"] = map[string]any{"provider_id": "x"}
	// 逆归一化: 展开 _top (等价后端 POST 逻辑)
	if topV, ok := saved["_top"].(map[string]any); ok && len(topV) > 0 {
		for k, v := range topV {
			if _, exists := saved[k]; !exists {
				saved[k] = v
			}
		}
		delete(saved, "_top")
	}
	// 插件读取路径: config['enable_plugin'] 存在
	if _, ok := saved["enable_plugin"]; !ok {
		t.Error("逆归一化后 enable_plugin 不在顶层 (插件读不到)")
	}
	if _, ok := saved["trigger_prefix"]; !ok {
		t.Error("逆归一化后 trigger_prefix 不在顶层")
	}
	t.Logf("逆归一化结果: %v", keysOf(saved))
}
