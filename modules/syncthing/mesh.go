// syncthing mesh — native gogitops function (user space, syncthing.* concept).
//
// Recipe step:
//   script: syncthing/mesh.go
//   script_args: "{{repo}}/recipes/syncthing-mesh/mesh.yaml {{hostname}}"
//
// What it does, in order:
//  1. Finds the local Syncthing REST API (config.xml: linux/mac paths).
//  2. Reads this node's device ID (GET /rest/system/status myID).
//  3. PUBLISHES the ID: `nenv set syncthing <roster-name> <id>` when the
//     node is part of the infra (nenv CLI present + writable); ALWAYS
//     prints syncthing_device_id=<id> so the recipe can set_attr
//     (Benn's rule: netenv when infra, attribute when no netenv).
//  4. RESOLVES the roster (mesh.yaml devices) — explicit ids win, then
//     netenv lookups, unresolvable names are skipped with a warning.
//  5. APPLIES additively: ensures roster devices exist locally; ensures
//     each declared folder exists (creates with path+devices) and that
//     declared peers are attached to it. NEVER removes devices or
//     folders, never rewrites an existing folder's path.
//
// Output: key=value lines for set_attr / human summary.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type meshDevice struct {
	Hostname string `yaml:"hostname"` // detected hostname → maps to this roster name
	ID       string `yaml:"id"`       // optional pre-seeded device ID
}

type meshFolder struct {
	ID      string   `yaml:"id"`
	Label   string   `yaml:"label"`
	Path    string   `yaml:"path"`
	Devices []string `yaml:"devices"` // roster names
}

type meshConfig struct {
	Devices map[string]meshDevice `yaml:"devices"`
	Folders []meshFolder          `yaml:"folders"`
}

// minimal YAML subset parser — flat two-level structure only (avoids yaml dep)
// NOTE: gogitops runner compiles modules standalone; keep stdlib-only.

func main() {
	args := os.Args[1:]
	meshPath := "recipes/syncthing-mesh/mesh.yaml"
	if len(args) > 0 && args[0] != "" {
		meshPath = args[0]
	}
	host := ""
	if len(args) > 1 {
		host = args[1]
	}
	if host == "" {
		h, _ := os.Hostname()
		host = h
	}

	apiBase, apiKey, err := localAPI()
	if err != nil {
		fmt.Println("syncthing_status=unreachable")
		fmt.Fprintln(os.Stderr, "mesh: no local syncthing API:", err)
		os.Exit(1)
	}

	myID, err := restString(apiBase, apiKey, "/rest/system/status", "myID")
	if err != nil {
		fmt.Println("syncthing_status=unreachable")
		fmt.Fprintln(os.Stderr, "mesh: status failed:", err)
		os.Exit(1)
	}

	cfg, err := parseMesh(meshPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mesh: config parse failed:", err)
		os.Exit(1)
	}

	// self roster-name: match by declared hostname, else by key
	selfName := ""
	for name, d := range cfg.Devices {
		if d.Hostname != "" && d.Hostname == host {
			selfName = name
			break
		}
	}
	if selfName == "" {
		for name, d := range cfg.Devices {
			if name == host || (d.Hostname == "" && d.ID == myID) {
				selfName = name
				break
			}
		}
	}

	// ── publish: netenv when part of the infra, attr always ──────────────
	published := "attr-only"
	if selfName != "" && haveNenv() {
		if out, err := runCmd("nenv", "set", "syncthing", selfName, myID); err != nil {
			fmt.Fprintf(os.Stderr, "mesh: nenv publish failed (falling back to attr): %v %s\n", err, out)
		} else {
			published = "netenv"
		}
	}
	fmt.Printf("syncthing_device_id=%s\n", myID)
	fmt.Printf("syncthing_publish=%s\n", published)
	if selfName != "" {
		fmt.Printf("syncthing_roster_name=%s\n", selfName)
	}

	// ── resolve roster IDs ───────────────────────────────────────────────
	ids := map[string]string{} // roster name → device ID
	for name, d := range cfg.Devices {
		if name == selfName {
			continue // self is implicit in local config
		}
		// netenv (self-published, freshest) wins; mesh.yaml id is the fallback seed
		resolved := ""
		if haveNenv() {
			if v, err := runCmd("nenv", "get", "syncthing", name); err == nil && isID(v) {
				resolved = strings.TrimSpace(v)
			}
		}
		if resolved == "" && d.ID != "" {
			resolved = d.ID
		}
		if resolved != "" {
			ids[name] = resolved
			continue
		}
		fmt.Fprintf(os.Stderr, "mesh: WARN device %q unresolvable (no netenv, no seed id) — skipping\n", name)
	}

	// ── apply: devices ───────────────────────────────────────────────────
	devicesAdded := 0
	existingDevices, _ := restListIDs(apiBase, apiKey, "/rest/config/devices")
	for name, id := range ids {
		if existingDevices[id] {
			continue
		}
		body := fmt.Sprintf(`{"deviceID":%q,"name":%q}`, id, name)
		if err := restPut(apiBase, apiKey, "/rest/config/devices/"+id, body); err != nil {
			fmt.Fprintf(os.Stderr, "mesh: WARN add device %s: %v\n", name, err)
			continue
		}
		devicesAdded++
	}

	// ── apply: folders (additive merge, raw-map — NEVER strip fields) ────
	foldersCreated, devicesAttached := 0, 0
	rawList, err := restGet(apiBase, apiKey, "/rest/config/folders")
	var folders []map[string]any
	if err == nil {
		_ = json.Unmarshal(rawList, &folders)
	}
	findFolder := func(id string) map[string]any {
		for _, f := range folders {
			if fid, _ := f["id"].(string); fid == id || strings.TrimSpace(fid) == strings.TrimSpace(id) {
				return f
			}
		}
		return nil
	}
	for _, f := range cfg.Folders {
		declaredIDs := []string{}
		for _, n := range f.Devices {
			if id, ok := ids[n]; ok {
				declaredIDs = append(declaredIDs, id)
			}
		}
		if selfName != "" {
			declaredIDs = append(declaredIDs, myID)
		}
		existing := findFolder(f.ID)
		if existing == nil {
			// create — raw map with sane defaults, exact declared id
			path := f.Path
			if strings.HasPrefix(path, "~/") {
				home, _ := os.UserHomeDir()
				path = filepath.Join(home, path[2:])
			}
			devs := []map[string]any{}
			for _, id := range declaredIDs {
				devs = append(devs, map[string]any{"deviceID": id, "introducedBy": "", "encryptionPassword": ""})
			}
			nf := map[string]any{
				"id": f.ID, "label": f.Label, "path": path, "type": "sendreceive",
				"rescanIntervalS": 3600, "fsWatcherEnabled": true, "fsWatcherDelayS": 10,
				"devices": devs,
			}
			b, _ := json.Marshal(nf)
			if err := restPut(apiBase, apiKey, "/rest/config/folders/"+url.PathEscape(f.ID), string(b)); err != nil {
				fmt.Fprintf(os.Stderr, "mesh: WARN create folder %q: %v\n", f.ID, err)
				continue
			}
			foldersCreated++
			continue
		}
		// merge: union devices into the RAW folder object, preserve everything else
		changed := false
		devs, _ := existing["devices"].([]any)
		has := map[string]bool{}
		for _, d := range devs {
			if dm, ok := d.(map[string]any); ok {
				if id, _ := dm["deviceID"].(string); id != "" {
					has[id] = true
				}
			}
		}
		for _, id := range declaredIDs {
			if !has[id] {
				devs = append(devs, map[string]any{"deviceID": id, "introducedBy": "", "encryptionPassword": ""})
				changed = true
				devicesAttached++
			}
		}
		if changed {
			existing["devices"] = devs
			b, _ := json.Marshal(existing)
			fid, _ := existing["id"].(string)
			if err := restPut(apiBase, apiKey, "/rest/config/folders/"+url.PathEscape(fid), string(b)); err != nil {
				fmt.Fprintf(os.Stderr, "mesh: WARN update folder %q: %v\n", fid, err)
			}
		}
	}

	fmt.Printf("syncthing_devices_added=%d\n", devicesAdded)
	fmt.Printf("syncthing_folders_created=%d\n", foldersCreated)
	fmt.Printf("syncthing_devices_attached=%d\n", devicesAttached)
	fmt.Println("syncthing_status=ok")
}

// ── local syncthing discovery ────────────────────────────────────────────

type xmlGUI struct {
	APIKey  string `xml:"apikey"`
	Address string `xml:"address"`
	TLS     string `xml:"tls,attr"`
}

type xmlConfig struct {
	GUI xmlGUI `xml:"gui"`
}

func localAPI() (string, string, error) {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local/state/syncthing/config.xml"),           // linux
		filepath.Join(home, ".config/syncthing/config.xml"),                // linux alt
		filepath.Join(home, "Library/Application Support/Syncthing/config.xml"), // mac
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c xmlConfig
		if err := xml.Unmarshal(raw, &c); err != nil {
			continue
		}
		if c.GUI.APIKey == "" {
			continue
		}
		addr := c.GUI.Address
		if addr == "" || strings.HasPrefix(addr, "0.0.0.0") {
			addr = "127.0.0.1:8384"
		}
		scheme := "http"
		if c.GUI.TLS == "true" {
			scheme = "https"
		}
		return scheme + "://" + addr, c.GUI.APIKey, nil
	}
	return "", "", fmt.Errorf("no syncthing config.xml with apikey found")
}

// ── REST helpers ─────────────────────────────────────────────────────────

func restString(base, key, path, field string) (string, error) {
	body, err := restGet(base, key, path)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return "", err
	}
	v, ok := m[field].(string)
	if !ok {
		return "", fmt.Errorf("field %s missing", field)
	}
	return v, nil
}

func restListIDs(base, key, path string) (map[string]bool, error) {
	body, err := restGet(base, key, path)
	out := map[string]bool{}
	if err != nil {
		return out, err
	}
	var devs []struct {
		DeviceID string `json:"deviceID"`
	}
	if json.Unmarshal(body, &devs) != nil {
		return out, nil
	}
	for _, d := range devs {
		out[d.DeviceID] = true
	}
	return out, nil
}

func restGet(base, key, path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("X-API-Key", key)
		cl := &http.Client{Timeout: 10 * time.Second}
		resp, err := cl.Do(req)
		if err != nil {
			lastErr = err
		} else {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 500 {
				if resp.StatusCode >= 400 {
					return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b)[:min(120, len(b))])
				}
				return b, nil
			}
			lastErr = fmt.Errorf("HTTP %d (config in flux?)", resp.StatusCode)
		}
		time.Sleep(600 * time.Millisecond)
	}
	return nil, lastErr
}

func restPut(base, key, path, body string) error {
	req, _ := http.NewRequest("PUT", base+path, strings.NewReader(body))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b)[:min(200, len(b))])
	}
	return nil
}

// ── netenv + misc ─────────────────────────────────────────────────────────

func haveNenv() bool {
	_, err := exec.LookPath("nenv")
	return err == nil
}

func runCmd(name string, a ...string) (string, error) {
	cmd := exec.Command(name, a...)
	out, err := cmd.Output()
	return string(out), err
}

func isID(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) == 63 || (len(s) >= 52 && strings.Count(s, "-") == 7)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── tiny YAML subset for mesh.yaml ───────────────────────────────────────
// Supports exactly:
//   devices:
//     <name>:
//       hostname: X
//       id: Y
//   folders:
//     - id: F
//       label: L
//       path: P
//       devices: [a, b, c]

func parseMesh(path string) (*meshConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &meshConfig{Devices: map[string]meshDevice{}}
	section := ""
	var curFolder *meshFolder
	var curDevice string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		t := strings.TrimSpace(line)
		switch {
		case indent == 0 && strings.HasSuffix(t, ":"):
			section = strings.TrimSuffix(t, ":")
		case indent == 2 && section == "devices" && strings.HasSuffix(t, ":"):
			curDevice = strings.TrimSuffix(t, ":")
			cfg.Devices[curDevice] = meshDevice{}
		case indent == 4 && section == "devices":
			k, v, ok := kv(t)
			if !ok || curDevice == "" {
				continue
			}
			d := cfg.Devices[curDevice]
			switch k {
			case "hostname":
				d.Hostname = v
			case "id":
				d.ID = v
			}
			cfg.Devices[curDevice] = d
		}
		// folder list entries — first field goes through kv() so quoted
		// values (e.g. ids with leading spaces) stay verbatim
		if section == "folders" && strings.HasPrefix(t, "- ") {
			k, v, ok := kv(strings.TrimPrefix(t, "- "))
			if !ok || k != "id" {
				continue
			}
			cfg.Folders = append(cfg.Folders, meshFolder{ID: v})
			curFolder = &cfg.Folders[len(cfg.Folders)-1]
			continue
		}
		if section == "folders" && curFolder != nil && indent >= 2 && !strings.HasPrefix(t, "-") {
			k, v, ok := kv(t)
			if !ok {
				continue
			}
			switch k {
			case "id":
				curFolder.ID = v
			case "label":
				curFolder.Label = v
			case "path":
				curFolder.Path = v
			case "devices":
				curFolder.Devices = parseList(v)
			}
		}
	}
	if len(cfg.Folders) == 0 && len(cfg.Devices) == 0 {
		return nil, fmt.Errorf("empty mesh config")
	}
	return cfg, nil
}

func kv(t string) (string, string, bool) {
	i := strings.Index(t, ":")
	if i < 0 {
		return "", "", false
	}
	k := strings.TrimSpace(t[:i])
	v := strings.TrimSpace(t[i+1:])
	// quoted values preserve their content VERBATIM (leading spaces are
	// real data here — syncthing folder ids can carry them)
	if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
		v = v[1 : len(v)-1]
	} else if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2 {
		v = v[1 : len(v)-1]
	}
	return k, v, k != ""
}

func parseList(v string) []string {
	v = strings.TrimSpace(strings.Trim(v, "[]"))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, `"' `))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
