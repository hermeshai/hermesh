package acp

import (
	"encoding/json"
	"reflect"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func TestParseElicitationForm_AntigravityMultiQuestion(t *testing.T) {
	raw := json.RawMessage(`{
		"mode": "form",
		"message": "The agent has 3 questions.",
		"requestedSchema": {
			"type": "object",
			"properties": {
				"q0": {
					"type": "string",
					"title": "本次迭代的推进节奏你倾向哪种？",
					"enum": ["(Recommended) 一次性完整落地", "分两阶段落地"]
				},
				"q0_other": {
					"type": "string",
					"description": "Optional free-form answer to: 本次迭代的推进节奏你倾向哪种？"
				},
				"q1": {
					"type": "array",
					"title": "哪些场景需要通知？",
					"items": {
						"type": "string",
						"enum": ["等待决策时", "任务失败时"]
					}
				},
				"q1_other": {
					"type": "string",
					"description": "Optional free-form answer to: 哪些场景需要通知？"
				},
				"q2": {
					"type": "string",
					"title": "默认终端如何选择？",
					"oneOf": [
						{"const": "auto", "title": "(Recommended) 零配置自动选择"},
						{"const": "setting", "title": "设置页下拉项"}
					]
				},
				"q2_other": {
					"type": "string",
					"description": "Optional free-form answer to: 默认终端如何选择？"
				}
			}
		}
	}`)

	items, metas, ok := parseElicitationForm(raw)
	if !ok {
		t.Fatalf("expected valid elicitation form")
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 merged QuestionItems (not 6), got %d: %+v", len(items), items)
	}

	if items[0].Id != "q0" || items[0].Text != "本次迭代的推进节奏你倾向哪种？" || items[0].Header != "" || !items[0].AllowOther || items[0].Optional || items[0].MultiSelect {
		t.Fatalf("unexpected item[0]: %+v", items[0])
	}
	if items[1].Id != "q1" || items[1].Text != "哪些场景需要通知？" || !items[1].MultiSelect || !items[1].AllowOther || items[1].Optional {
		t.Fatalf("unexpected item[1]: %+v", items[1])
	}
	if items[2].Id != "q2" || items[2].Text != "默认终端如何选择？" || items[2].Header != "" || !items[2].AllowOther || items[2].Optional {
		t.Fatalf("unexpected item[2]: %+v", items[2])
	}

	content := buildElicitationContent(metas, map[string]*sessionv1.QuestionAnswer{
		"q0": {CustomText: "先做 P0 再看效果"},
		"q1": {SelectedOptions: []string{"等待决策时", "任务失败时"}},
		"q2": {SelectedOptions: []string{"(Recommended) 零配置自动选择"}},
	})
	want := map[string]any{
		"q0_other": "先做 P0 再看效果",
		"q1":       []string{"等待决策时", "任务失败时"},
		"q2":       "auto",
	}
	if !reflect.DeepEqual(content, want) {
		t.Fatalf("content mismatch:\ngot:  %#v\nwant: %#v", content, want)
	}
}

func TestParseElicitationForm_PreservesGenericFormFields(t *testing.T) {
	raw := json.RawMessage(`{
		"mode": "form",
		"message": "选哪个颜色",
		"requestedSchema": {
			"type": "object",
			"required": ["choice"],
			"properties": {
				"choice": {
					"type": "string",
					"title": "Choice",
					"oneOf": [
						{"const": "v-红", "title": "红"},
						{"const": "v-蓝", "title": "蓝"}
					]
				},
				"note": {
					"type": "string",
					"title": "Note",
					"description": "Anything else"
				}
			}
		}
	}`)

	items, metas, ok := parseElicitationForm(raw)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 items for generic form, got ok=%v items=%+v", ok, items)
	}
	if items[0].Header != "Choice" || items[0].Text != "选哪个颜色" || items[0].Optional || items[0].AllowOther {
		t.Fatalf("unexpected choice item: %+v", items[0])
	}
	if items[1].Header != "Note" || items[1].Text != "Anything else" || !items[1].Optional || !items[1].AllowOther {
		t.Fatalf("unexpected note item: %+v", items[1])
	}
	content := buildElicitationContent(metas, map[string]*sessionv1.QuestionAnswer{
		"choice": {SelectedOptions: []string{"红"}},
	})
	if content["choice"] != "v-红" {
		t.Fatalf("expected v-红, got %#v", content)
	}
}
