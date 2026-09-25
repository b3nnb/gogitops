package health

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HardwareSpecs is the static hardware inventory an agent collects once at
// startup and embeds in /v1/health. Every field is best-effort: absent
// sources leave the field empty rather than failing collection.
type HardwareSpecs struct {
	CPU         string     `json:"cpu,omitempty"`         // full model string, e.g. "AMD Ryzen 9 5900X 12-Core Processor"
	CPUCores    int        `json:"cpu_cores,omitempty"`   // logical cores
	RAMGB       int        `json:"ram_gb,omitempty"`      // total RAM, rounded up to GB
	RAMType     string     `json:"ram_type,omitempty"`    // DDR4 / DDR5 / LPDDR5 (dmidecode on Linux, best-effort)
	GPUs        []GPUInfo  `json:"gpus,omitempty"`        // nvidia-smi when present, else lspci
	Disks       []DiskInfo `json:"disks,omitempty"`       // physical disks (lsblk TYPE=disk on Linux)
	Motherboard string     `json:"motherboard,omitempty"` // board vendor+name (Linux DMI) or hw.model (macOS)
}

// GPUInfo is one GPU as reported by nvidia-smi (model + VRAM + driver)
// or lspci (model only).
type GPUInfo struct {
	Model  string `json:"model,omitempty"`
	VRAMMB int    `json:"vram_mb,omitempty"` // MiB, nvidia-smi only
	Driver string `json:"driver,omitempty"`  // nvidia driver version, nvidia-smi only
}

// DiskInfo is one physical disk (not partitions).
type DiskInfo struct {
	Model      string `json:"model,omitempty"`
	SizeGB     int    `json:"size_gb,omitempty"`
	Rotational bool   `json:"rotational,omitempty"` // true = spinning rust
}

// CollectHardwareSpecs gathers the node's hardware inventory. Runs once per
// agent process at startup — individual probes have short timeouts and never
// return errors; whatever is unavailable simply stays empty.
func CollectHardwareSpecs() HardwareSpecs {
	var hw HardwareSpecs
	switch runtime.GOOS {
	case "linux":
		hw = collectLinux()
	case "darwin":
		hw = collectDarwin()
	}
	return hw
}

// ── Linux ─────────────────────────────────────────────────────────────────

func collectLinux() HardwareSpecs {
	var hw HardwareSpecs

	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		hw.CPU, hw.CPUCores = parseCPUInfo(string(data))
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		hw.RAMGB = gbFromKB(parseMemTotalKB(string(data)))
	}
	hw.RAMType = linuxRAMType()
	hw.GPUs = linuxGPUs()
	hw.Disks = linuxDisks()
	hw.Motherboard = linuxBoard()

	return hw
}

// linuxRAMType asks dmidecode for the populated memory type. Root-only tool —
// root-run agents get DDR4/DDR5 directly; user-run agents fall back to a
// passwordless `sudo -n dmidecode` probe (never prompts, silent when the
// sudoers policy would require a password) and omit the type otherwise.
func linuxRAMType() string {
	if out, err := runTimed(3*time.Second, "dmidecode", "-t", "memory"); err == nil {
		return parseDMIType(out)
	}
	if out, err := runTimed(3*time.Second, "sudo", "-n", "dmidecode", "-t", "memory"); err == nil {
		return parseDMIType(out)
	}
	return ""
}

// linuxGPUs prefers nvidia-smi (authoritative model + VRAM + driver) and
// falls back to lspci for whatever nvidia-smi doesn't cover.
func linuxGPUs() []GPUInfo {
	var gpus []GPUInfo
	nvidia := map[string]bool{} // models already reported via nvidia-smi

	if out, err := runTimed(4*time.Second, "nvidia-smi", "--query-gpu=name,memory.total,driver_version", "--format=csv,noheader"); err == nil {
		for _, g := range parseNvidiaSMI(out) {
			if g.Model != "" {
				nvidia[strings.ToLower(g.Model)] = true
				gpus = append(gpus, g)
			}
		}
	}

	if out, err := runTimed(3*time.Second, "lspci", "-mm"); err == nil {
		for _, model := range parseLSPCI(out) {
			if model == "" || nvidia[strings.ToLower(model)] {
				continue
			}
			// Skip nvidia-smi-reported cards that lspci also lists.
			if strings.HasPrefix(strings.ToLower(model), "nvidia ") && len(nvidia) > 0 {
				continue
			}
			// iGPU clutter: when a discrete GPU already exists, the
			// onboard Intel/AMD graphics tile is noise, not inventory.
			if len(gpus) > 0 && isIGPU(model) {
				continue
			}
			gpus = append(gpus, GPUInfo{Model: model})
		}
	}
	return gpus
}

// isIGPU recognizes onboard graphics tiles by their lspci names.
func isIGPU(model string) bool {
	patterns := []string{
		"AlderLake", "RaptorLake", "MeteorLake", "LunarLake", "ArrowLake",
		"GT1", "GT2", "UHD Graphics", "Iris ", "HD Graphics",
		"Raphael", "Phoenix", "Cezanne", "Rembrandt", "Renoir", "Picasso",
		"Barcelo", "Van Gogh", "Vega Graphics", "ASPEED", "Matrox", "virtio",
	}
	m := strings.ToLower(model)
	for _, p := range patterns {
		if strings.Contains(m, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func linuxDisks() []DiskInfo {
	out, err := runTimed(3*time.Second, "lsblk", "-dbno", "NAME,SIZE,TYPE,ROTA,MODEL")
	if err != nil {
		return nil
	}
	return parseLSBLK(out)
}

// boardVendorShort shortens the verbose DMI board vendor strings.
var boardVendorShort = []struct{ long, short string }{
	{"ASUSTeK COMPUTER INC.", "ASUS"},
	{"Micro-Star International Co., Ltd.", "MSI"},
	{"Gigabyte Technology Co., Ltd.", "GIGABYTE"},
	{"ASRock Incorporation", "ASRock"},
	{"ASRock", "ASRock"},
	{"Dell Inc.", "Dell"},
	{"Hewlett-Packard", "HP"},
	{"HP Inc.", "HP"},
	{"Lenovo", "Lenovo"},
	{"Supermicro", "Supermicro"},
	{"To Be Filled By O.E.M.", ""},
}

// shortBoard trims vendor noise: "ASUSTeK COMPUTER INC. ROG STRIX Z790-I
// GAMING WIFI" → "ASUS ROG STRIX Z790-I GAMING WIFI".
func shortBoard(vendor, name string) string {
	for _, v := range boardVendorShort {
		if vendor == v.long {
			vendor = v.short
			break
		}
	}
	if vendor == "" {
		return name
	}
	if name == "" || strings.EqualFold(name, "To be filled by O.E.M.") ||
		strings.Contains(strings.ToLower(name), "default string") {
		return vendor
	}
	return strings.TrimSpace(vendor + " " + name)
}

func linuxBoard() string {
	vendor := readTrim("/sys/devices/virtual/dmi/id/board_vendor")
	name := readTrim("/sys/devices/virtual/dmi/id/board_name")
	if board := shortBoard(vendor, name); board != "" {
		return board
	}
	return readTrim("/sys/devices/virtual/dmi/id/product_name")
}

// ── macOS ─────────────────────────────────────────────────────────────────

func collectDarwin() HardwareSpecs {
	var hw HardwareSpecs

	if out, err := runTimed(3*time.Second, "sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
		hw.CPU = strings.TrimSpace(out)
	}
	if hw.CPU == "" {
		// Apple Silicon has no brand_string — use the chip name from
		// system_profiler instead (collected below when it's fast enough).
		hw.CPU = darwinChip()
	}
	if out, err := runTimed(3*time.Second, "sysctl", "-n", "hw.ncpu"); err == nil {
		hw.CPUCores, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	if out, err := runTimed(3*time.Second, "sysctl", "-n", "hw.memsize"); err == nil {
		if bytes, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			hw.RAMGB = gbFromBytes(bytes)
		}
	}
	hw.GPUs = darwinGPUs()
	hw.Motherboard = darwinModel()
	hw.Disks = darwinDisks()

	return hw
}

func darwinChip() string {
	out, err := runTimed(8*time.Second, "system_profiler", "SPHardwareDataType")
	if err != nil {
		return ""
	}
	return parseSPHardwareChip(out)
}

func darwinGPUs() []GPUInfo {
	out, err := runTimed(8*time.Second, "system_profiler", "SPDisplaysDataType")
	if err != nil {
		return nil
	}
	return parseSPDisplays(out)
}

func darwinModel() string {
	out, err := runTimed(3*time.Second, "sysctl", "-n", "hw.model")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// darwinDisks reports the root physical volume via diskutil (best-effort).
func darwinDisks() []DiskInfo {
	out, err := runTimed(5*time.Second, "diskutil", "info", "/")
	if err != nil {
		return nil
	}
	size, model := parseDiskutil(out)
	if size == 0 {
		return nil
	}
	return []DiskInfo{{Model: model, SizeGB: size}}
}

// ── Shared helpers ────────────────────────────────────────────────────────

// runTimed runs a command with a hard timeout and returns trimmed stdout.
func runTimed(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func gbFromKB(kb int) int {
	if kb <= 0 {
		return 0
	}
	// Nearest GiB, then snap to the nearest DIMM-combo size within 4% —
	// BIOS reservations shave a real 64GB machine down to ~62.6 GiB, and
	// "64GB" is what the machine actually IS.
	return snapRAM(int(math.Round(float64(kb) * 1024 / (1024 * 1024 * 1024))))
}

// ramSnaps covers common DIMM/module combos (4GB steps to 512, then big ones).
var ramSnaps = func() []int {
	var out []int
	for s := 4; s <= 512; s += 4 {
		out = append(out, s)
	}
	return append(out, 640, 768, 1024, 1536, 2048)
}()

// snapRAM rounds gb to the nearest snap candidate within 4% (1/25).
func snapRAM(gb int) int {
	for _, s := range ramSnaps {
		d := gb - s
		if d < 0 {
			d = -d
		}
		if d*25 <= s {
			return s
		}
	}
	return gb
}

func gbFromBytes(b int64) int {
	if b <= 0 {
		return 0
	}
	return int(math.Round(float64(b) / (1024 * 1024 * 1024))) // nearest GiB
}

// ── Parsers (pure functions — unit-tested in specs_test.go) ────────────────

var modelRe = regexp.MustCompile(`(?m)^model name\s*:\s*(.+)$`)

// parseCPUInfo returns the CPU model (first "model name" line) and the
// logical core count ("processor" lines).
func parseCPUInfo(data string) (string, int) {
	model := ""
	if m := modelRe.FindStringSubmatch(data); m != nil {
		model = strings.TrimSpace(m[1])
	}
	cores := strings.Count(data, "\nprocessor")
	if strings.HasPrefix(data, "processor") {
		cores++
	}
	return model, cores
}

// parseMemTotalKB extracts MemTotal from /proc/meminfo (kB).
func parseMemTotalKB(data string) int {
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb
			}
		}
	}
	return 0
}

// parseNvidiaSMI parses "name, memory.total, driver_version" CSV rows:
//
//	NVIDIA GeForce RTX 4070, 12288 MiB, 550.107.02
func parseNvidiaSMI(out string) []GPUInfo {
	var gpus []GPUInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ",") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			continue
		}
		g := GPUInfo{
			Model:  shortGPU(strings.TrimSpace(parts[0])),
			Driver: strings.TrimSpace(parts[2]),
		}
		mem := strings.Fields(strings.TrimSpace(parts[1]))
		if len(mem) >= 1 {
			v, _ := strconv.Atoi(mem[0])
			g.VRAMMB = v
		}
		if g.Model != "" {
			gpus = append(gpus, g)
		}
	}
	return gpus
}

// gpuClassRe matches the quoted device class in `lspci -mm` output (mid-line):
// "VGA compatible controller", "3D controller", "Display controller".
var gpuClassRe = regexp.MustCompile(`"(VGA compatible controller|3D controller|Display controller)"`)

// parseLSPCI extracts GPU models from `lspci -mm` output. Lines look like:
//
//	01:00.0 "VGA compatible controller" "NVIDIA Corporation" "GA104 [GeForce RTX 3070]" -r01 "ASUSTeK Computer Inc." "Device 87c3"
func parseLSPCI(out string) []string {
	var models []string
	for _, line := range strings.Split(out, "\n") {
		if !gpuClassRe.MatchString(line) {
			continue
		}
		// Quoted fields: class, vendor, device description, ...
		quoted := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(line, -1)
		if len(quoted) < 3 {
			continue
		}
		desc := quoted[2][1]
		// Strip bracketed raw chip names: "GA104 [GeForce RTX 3070]" → "GeForce RTX 3070"
		if m := regexp.MustCompile(`\[(.+)\]`).FindStringSubmatch(desc); m != nil {
			desc = m[1]
		}
		desc = shortGPU(desc)
		if desc != "" {
			models = append(models, desc)
		}
	}
	return models
}

// shortGPU trims vendor noise for compact display:
// "NVIDIA GeForce RTX 4070" → "RTX 4070", "AMD/ATI Raphael" stays as-is.
func shortGPU(model string) string {
	s := strings.TrimSpace(model)
	for _, prefix := range []string{"NVIDIA ", "nvidia ", "NVIDIA Corporation "} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimPrefix(s, "GeForce ")
	return strings.TrimSpace(s)
}

// parseLSBLK parses `lsblk -dbno NAME,SIZE,TYPE,ROTA,MODEL` output:
//
//	nvme0n1 2000398934016 disk 0 Samsung Electronics Co Ltd NVMe SSD Controller SM981a
func parseLSBLK(out string) []DiskInfo {
	var disks []DiskInfo
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// NAME SIZE TYPE ROTA MODEL...  — MODEL may be absent/empty
		if len(fields) < 4 || fields[2] != "disk" {
			continue
		}
		name := fields[0]
		// Skip virtual/ephemeral devices: zram, loop, ram, dm-, sr (cdrom)
		if strings.HasPrefix(name, "zram") || strings.HasPrefix(name, "loop") ||
			strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") ||
			strings.HasPrefix(name, "sr") || strings.HasPrefix(name, "md") {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		if size <= 0 {
			continue
		}
		gb := gbFromBytes(size)
		if gb == 0 {
			continue // sub-GB devices (zram remnants, tiny loop mounts)
		}
		rota := fields[3] == "1"
		model := shortDiskModel(strings.Join(fields[4:], " "))
		disks = append(disks, DiskInfo{
			Model:      model,
			SizeGB:     gb,
			Rotational: rota,
		})
	}
	return disks
}

var diskVendorRe = regexp.MustCompile(`^(Samsung Electronics|Seagate Technology|Hewlett-Packard|Western Digital|TOSHIBA|INTEL)\b`)
var diskLegalRe = regexp.MustCompile(`\s+(Co\.?,? ?Ltd\.?|Corporation|Inc\.?|Company|Co\.)\s*`)

// shortDiskModel trims legal-entity noise for compact display:
// "Samsung Electronics Co Ltd NVMe SSD Controller SM981a" →
// "Samsung NVMe SSD Controller SM981a".
func shortDiskModel(model string) string {
	if model == "" {
		return model
	}
	model = diskLegalRe.ReplaceAllString(model, " ")
	if v := diskVendorRe.FindString(model); v != "" {
		model = diskVendorShort[v] + model[len(v):]
	}
	return strings.TrimSpace(strings.Join(strings.Fields(model), " "))
}

// diskVendorShort shortens the verbose lsblk vendor prefixes.
var diskVendorShort = map[string]string{
	"Samsung Electronics": "Samsung",
	"Seagate Technology":  "Seagate",
	"Hewlett-Packard":     "HP",
	"Western Digital":     "WD",
	"TOSHIBA":             "Toshiba",
	"INTEL":               "Intel",
}

// parseDMIType extracts the populated memory type from dmidecode -t memory:
// prefers the first concrete "Type: DDR4" over "Unknown"/"Other".
func parseDMIType(out string) string {
	best := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Type:") {
			continue
		}
		t := strings.TrimSpace(strings.TrimPrefix(line, "Type:"))
		if t == "" || strings.EqualFold(t, "Unknown") || strings.EqualFold(t, "Other") {
			continue
		}
		if best == "" {
			best = t
		}
	}
	return best
}

var spChipRe = regexp.MustCompile(`(?m)^\s*Chip:\s*(.+)$`)

// parseSPHardwareChip pulls "Chip: Apple M2 Pro" from system_profiler.
func parseSPHardwareChip(out string) string {
	if m := spChipRe.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// parseSPDisplays pulls GPU chipset + VRAM from system_profiler
// SPDisplaysDataType ("Chipset Model: Apple M2 Pro", "VRAM (Total): 16 GB").
func parseSPDisplays(out string) []GPUInfo {
	var gpus []GPUInfo
	var cur *GPUInfo
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Chipset Model:") {
			if cur != nil && cur.Model != "" {
				gpus = append(gpus, *cur)
			}
			cur = &GPUInfo{Model: strings.TrimSpace(strings.TrimPrefix(trimmed, "Chipset Model:"))}
		} else if cur != nil && strings.HasPrefix(trimmed, "VRAM (Total):") {
			v := strings.Fields(strings.TrimPrefix(trimmed, "VRAM (Total):"))
			if len(v) > 0 {
				if mb, err := strconv.Atoi(v[0]); err == nil {
					cur.VRAMMB = mb * 1024 // reported in GB on modern macOS
					if len(v) > 1 && strings.EqualFold(v[1], "MB") {
						cur.VRAMMB = mb
					}
				}
			}
		}
	}
	if cur != nil && cur.Model != "" {
		gpus = append(gpus, *cur)
	}
	return gpus
}

var duSizeRe = regexp.MustCompile(`(?m)^\s*(?:Total Size|Disk Size|Total Non-Apple-Size):\s*([\d.]+)\s*([KMGTP]?B?)`)
var duModelRe = regexp.MustCompile(`(?m)^\s*(?:Device Name|Volume Name|Device / Media Name):\s*(.+)$`)

// parseDiskutil pulls the root disk total size (bytes) + name from
// `diskutil info /`.
func parseDiskutil(out string) (intGB int, model string) {
	if m := duSizeRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			mult := map[string]float64{"": 1, "B": 1, "KB": 1024, "MB": 1024 * 1024, "GB": 1024 * 1024 * 1024}[strings.ToUpper(m[2])]
			if mult == 0 {
				mult = 1
			}
			return gbFromBytes(int64(v * mult)), ""
		}
	}
	if m := duModelRe.FindStringSubmatch(out); m != nil {
		model = strings.TrimSpace(m[1])
	}
	return 0, model
}

// ── Display helpers ───────────────────────────────────────────────────────

// coreSuffixRe strips the redundant "12-Core" tail — the core count is
// displayed separately.
var coreSuffixRe = regexp.MustCompile(` \d+[- ][Cc]ores?$`)

// genPrefixRe strips "12th Gen " style prefixes from Intel cpuinfo strings.
var genPrefixRe = regexp.MustCompile(`^\d+(st|nd|rd|th) Gen `)

// ShortCPU trims vendor/register noise for compact display:
//
//	"AMD Ryzen 9 5900X 12-Core Processor"                → "Ryzen 9 5900X"
//	"12th Gen Intel(R) Core(TM) i9-12900K"                → "Core i9-12900K"
//	"Intel(R) Core(TM) i7-13700K CPU @ 3.40GHz"           → "Core i7-13700K"
//	"Apple M2 Pro"                                        → "Apple M2 Pro"
func ShortCPU(model string) string {
	s := strings.TrimSpace(model)
	s = genPrefixRe.ReplaceAllString(s, "")
	for _, noise := range []string{"(R)", "(TM)", "(C)", "(r)", "(tm)"} {
		s = strings.ReplaceAll(s, noise, "")
	}
	s = strings.TrimPrefix(s, "Intel ")
	s = strings.TrimPrefix(s, "AMD ")
	for _, cut := range []string{" CPU", " Processor", " with Radeon", " with Iris", " @"} {
		if i := strings.Index(s, cut); i >= 0 {
			s = s[:i]
		}
	}
	s = coreSuffixRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ") // collapse doubled spaces
}

// CompactSpecs renders the one-line hardware summary for dashboard cards:
//
//	Ryzen 9 5900X · 24c · 64GB DDR4 · RTX 4070 12GB drv 550.107.02 · 5.5T · ROG STRIX B550-I
//
// Empty fields are skipped entirely — a partial inventory renders a short
// line, an absent one renders "".
func CompactSpecs(hw *HardwareSpecs) string {
	if hw == nil {
		return ""
	}
	parts := []string{}
	if hw.CPU != "" {
		cpu := ShortCPU(hw.CPU)
		if hw.CPUCores > 0 {
			cpu += fmt.Sprintf(" %dc", hw.CPUCores)
		}
		parts = append(parts, cpu)
	}
	if hw.RAMGB > 0 {
		ram := strconv.Itoa(hw.RAMGB) + "GB"
		if hw.RAMType != "" {
			ram += " " + hw.RAMType
		}
		parts = append(parts, ram)
	}
	for _, g := range hw.GPUs {
		gpu := g.Model
		if g.VRAMMB > 0 {
			gpu += " " + strconv.Itoa((g.VRAMMB+512)/1024) + "GB"
		}
		if g.Driver != "" {
			gpu += " drv " + g.Driver
		}
		parts = append(parts, gpu)
	}
	if total := diskTotalBytes(hw.Disks); total > 0 {
		parts = append(parts, humanBytes(total))
	}
	if hw.Motherboard != "" {
		parts = append(parts, hw.Motherboard)
	}
	return strings.Join(parts, " · ")
}

// FullSpecs renders the tooltip variant with untruncated disk details.
func FullSpecs(hw *HardwareSpecs) string {
	if hw == nil {
		return ""
	}
	parts := []string{}
	if hw.CPU != "" {
		parts = append(parts, "CPU: "+hw.CPU)
	}
	if hw.RAMGB > 0 {
		ram := "RAM: " + strconv.Itoa(hw.RAMGB) + "GB"
		if hw.RAMType != "" {
			ram += " " + hw.RAMType
		}
		parts = append(parts, ram)
	}
	for _, g := range hw.GPUs {
		gpu := "GPU: " + g.Model
		if g.VRAMMB > 0 {
			gpu += " (" + strconv.Itoa(g.VRAMMB) + " MiB)"
		}
		if g.Driver != "" {
			gpu += " driver " + g.Driver
		}
		parts = append(parts, gpu)
	}
	for _, d := range hw.Disks {
		disk := "Disk: " + d.Model
		if disk == "Disk: " {
			disk = "Disk:"
		}
		disk += " " + humanBytes(int64(d.SizeGB)*1024*1024*1024)
		if d.Rotational {
			disk += " (HDD)"
		}
		parts = append(parts, disk)
	}
	if hw.Motherboard != "" {
		parts = append(parts, "Board: "+hw.Motherboard)
	}
	return strings.Join(parts, "\n")
}

func diskTotalBytes(disks []DiskInfo) int64 {
	var total int64
	for _, d := range disks {
		total += int64(d.SizeGB) * 1024 * 1024 * 1024
	}
	return total
}

// humanBytes renders 1.9T / 5.5T / 812G style sizes.
func humanBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024*1024:
		return strconv.FormatFloat(float64(b)/(1024*1024*1024*1024), 'f', 1, 64) + "T"
	case b >= 1024*1024*1024:
		return strconv.Itoa(int(b)/(1024*1024*1024)) + "G"
	default:
		return strconv.Itoa(int(b)/(1024*1024)) + "M"
	}
}
