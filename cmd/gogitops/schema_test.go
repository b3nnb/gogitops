package main

import (
	"encoding/json"
	"testing"
)

func TestSchemaEmitsValidJSON(t *testing.T) {
	data, err := json.Marshal(buildRecipeSchema(nil, nil))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if back["$schema"] == nil || back["title"] == nil {
		t.Fatal("missing $schema/title")
	}
	req := back["required"].([]any)
	if len(req) != 2 || req[0] != "name" || req[1] != "steps" {
		t.Fatalf("required = %v", req)
	}
}

func TestSchemaStrictOnBothLevels(t *testing.T) {
	data, _ := json.Marshal(buildRecipeSchema(nil, nil))
	var back map[string]any
	json.Unmarshal(data, &back)
	if back["additionalProperties"] != false {
		t.Fatal("top level must be strict")
	}
	step := back["properties"].(map[string]any)["steps"].(map[string]any)["items"].(map[string]any)
	if step["additionalProperties"] != false {
		t.Fatal("steps must be strict")
	}
	sreq := step["required"].([]any)
	if len(sreq) != 1 || sreq[0] != "name" {
		t.Fatalf("step required = %v", sreq)
	}
}

func TestSchemaStepVocabulary(t *testing.T) {
	data, _ := json.Marshal(buildRecipeSchema(nil, nil))
	var back map[string]any
	json.Unmarshal(data, &back)
	step := back["properties"].(map[string]any)["steps"].(map[string]any)["items"].(map[string]any)
	props := step["properties"].(map[string]any)
	for _, k := range []string{
		"name", "command", "script", "script_args", "script_lang",
		"package", "sources", "schedule", "os", "arch",
		"expect", "expect_regex", "expect_exit", "parse", "pattern",
		"only_if", "when", "on_failure", "retries", "retry_delay",
		"assert", "set_attr", "attr_prefix", "when_attr", "only_if_attr",
		"labels_required", "labels_exclude",
		"mount", "device", "at", "options", "fstab",
	} {
		if props[k] == nil {
			t.Fatalf("step schema missing key: %s", k)
		}
	}
	if props["script_lang"].(map[string]any)["enum"] == nil {
		t.Fatal("script_lang must be an enum")
	}
}

func TestSchemaEnumInjection(t *testing.T) {
	data, _ := json.Marshal(buildRecipeSchema([]string{"gpu", "hermes-host"}, []string{"friday", "mini"}))
	var back map[string]any
	json.Unmarshal(data, &back)
	props := back["properties"].(map[string]any)

	labels := props["labels"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	if len(labels) != 2 || labels[0] != "gpu" || labels[1] != "hermes-host" {
		t.Fatalf("labels enum = %v", labels)
	}
	req := props["steps"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["labels_required"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	if len(req) != 2 {
		t.Fatalf("labels_required enum = %v", req)
	}
	hosts := props["node_overrides"].(map[string]any)["propertyNames"].(map[string]any)["enum"].([]any)
	if len(hosts) != 2 || hosts[0] != "friday" {
		t.Fatalf("node_overrides propertyNames enum = %v", hosts)
	}
}

func TestSchemaNoEnumsWithoutRepo(t *testing.T) {
	data, _ := json.Marshal(buildRecipeSchema(nil, nil))
	var back map[string]any
	json.Unmarshal(data, &back)
	props := back["properties"].(map[string]any)
	if props["node_overrides"].(map[string]any)["propertyNames"] != nil {
		t.Fatal("propertyNames should be absent without hostnames")
	}
	items := props["labels"].(map[string]any)["items"].(map[string]any)
	if items["enum"] != nil {
		t.Fatal("labels enum should be absent without fleet data")
	}
}
