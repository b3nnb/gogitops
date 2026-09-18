package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ApplySelfTags adds/removes labels on THIS node's own nodes/<hostname>.yaml.
//
// Semantics:
//   - Idempotent: re-running with the same add/remove set changes nothing.
//   - Comment-preserving: the edit walks the yaml.v3 Node tree and rewrites
//     ONLY the labels sequence — every comment and every other key in the
//     file survives byte-for-byte style intact (hand-written files keep
//     their annotations).
//   - Key-tolerant: edits whichever of `labels:` (machine-written style) or
//     `tags:` (hand-written style) already exists in the file; appends
//     `labels:` when neither does. LoadNode normalizes `tags:` → Labels,
//     so both spellings are live config.
//   - Self-scoped: only ever touches nodes/<hostname>.yaml — a recipe can
//     never edit another node's tags.
//
// Returns (before, after, changed, error).
func ApplySelfTags(repoDir, hostname string, add, remove []string) ([]string, []string, bool, error) {
	path := filepath.Join(repoDir, "nodes", hostname+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("node config not found for %s — the node must be enrolled before a recipe can manage its tags: %w", hostname, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil, false, fmt.Errorf("empty document: %s", path)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, false, fmt.Errorf("unexpected root node (want mapping): %s", path)
	}

	// Locate the labels sequence: prefer `labels:`, fall back to `tags:`
	// (both spellings appear in the fleet — machine-written vs hand-written).
	var keyName string
	var labelsVal *yaml.Node
	labelsIdx := -1
	for _, candidate := range []string{"labels", "tags"} {
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == candidate {
				keyName = candidate
				labelsVal = root.Content[i+1]
				labelsIdx = i
				break
			}
		}
		if labelsIdx >= 0 {
			break
		}
	}

	var before []string
	if labelsVal != nil && labelsVal.Kind == yaml.SequenceNode {
		for _, item := range labelsVal.Content {
			before = append(before, item.Value)
		}
	}

	// Compute the new list: existing order preserved, removes dropped,
	// adds appended (dedup on both sides).
	removeSet := map[string]bool{}
	for _, t := range remove {
		if t != "" {
			removeSet[t] = true
		}
	}
	after := []string{}
	seen := map[string]bool{}
	for _, t := range before {
		if removeSet[t] || seen[t] || t == "" {
			continue
		}
		seen[t] = true
		after = append(after, t)
	}
	for _, t := range add {
		if seen[t] || t == "" {
			continue
		}
		seen[t] = true
		after = append(after, t)
	}

	same := len(after) == len(before)
	for i := 0; same && i < len(after); i++ {
		if after[i] != before[i] {
			same = false
		}
	}
	if same {
		return before, after, false, nil
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, t := range after {
		seq.Content = append(seq.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: t,
		})
	}
	if labelsIdx >= 0 {
		root.Content[labelsIdx+1] = seq
	} else {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"},
			seq)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return before, after, false, err
	}
	enc.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		return before, after, false, err
	}
	_ = keyName // (diagnostic only)
	return before, after, true, nil
}
