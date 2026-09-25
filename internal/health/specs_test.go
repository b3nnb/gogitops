package health

import (
	"strings"
	"testing"
)

const sampleCPUInfo = `processor	: 0
vendor_id	: AuthenticAMD
cpu family	: 25
model name	: AMD Ryzen 9 5900X 12-Core Processor
stepping	: 0
processor	: 1
model name	: AMD Ryzen 9 5900X 12-Core Processor
processor	: 2
model name	: AMD Ryzen 9 5900X 12-Core Processor
`

const sampleCPUInfoIntel = `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Core(TM) i7-13700K CPU @ 3.40GHz
processor	: 1
processor	: 2
processor	: 3
`

const sampleMemInfo = `MemTotal:       65784232 kB
MemFree:         1234567 kB
MemAvailable:   32123456 kB
`

func TestParseCPUInfo(t *testing.T) {
	model, cores := parseCPUInfo(sampleCPUInfo)
	if model != "AMD Ryzen 9 5900X 12-Core Processor" {
		t.Errorf("model = %q", model)
	}
	if cores != 3 {
		t.Errorf("cores = %d, want 3", cores)
	}

	model, cores = parseCPUInfo(sampleCPUInfoIntel)
	if model != "Intel(R) Core(TM) i7-13700K CPU @ 3.40GHz" {
		t.Errorf("intel model = %q", model)
	}
	if cores != 4 {
		t.Errorf("intel cores = %d, want 4", cores)
	}

	// Empty / ARM-style cpuinfo (no model name): model empty, cores still count
	model, cores = parseCPUInfo("processor\t: 0\nprocessor\t: 1\n")
	if model != "" || cores != 2 {
		t.Errorf("arm cpuinfo: model=%q cores=%d, want \"\"/2", model, cores)
	}
}

func TestParseMemTotalKB(t *testing.T) {
	kb := parseMemTotalKB(sampleMemInfo)
	if kb != 65784232 {
		t.Errorf("kb = %d", kb)
	}
	// Exact: 65784232 kB = 62.65 GiB → nearest 63 → snaps to 64 (DIMM reality)
	if got := gbFromKB(65784232); got != 64 {
		t.Errorf("gbFromKB = %d, want 64", got)
	}
	// Exact power-of-two hardware (e.g. 16GB mini): hw.memsize 17179869184 B
	if got := gbFromBytes(17179869184); got != 16 {
		t.Errorf("gbFromBytes(16GiB) = %d, want 16", got)
	}
	if parseMemTotalKB("garbage") != 0 {
		t.Errorf("garbage should be 0")
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	out := "NVIDIA GeForce RTX 4070, 12288 MiB, 550.107.02\nNVIDIA GeForce RTX 3060,  8192 MiB, 550.107.02\n"
	gpus := parseNvidiaSMI(out)
	if len(gpus) != 2 {
		t.Fatalf("gpus = %d, want 2", len(gpus))
	}
	if gpus[0].Model != "RTX 4070" {
		t.Errorf("model = %q, want RTX 4070", gpus[0].Model)
	}
	if gpus[0].VRAMMB != 12288 {
		t.Errorf("vram = %d", gpus[0].VRAMMB)
	}
	if gpus[0].Driver != "550.107.02" {
		t.Errorf("driver = %q", gpus[0].Driver)
	}
	if gpus[1].Model != "RTX 3060" || gpus[1].VRAMMB != 8192 {
		t.Errorf("gpu2 = %+v", gpus[1])
	}

	// Malformed rows are skipped, not fatal
	if got := parseNvidiaSMI("junk\n\n"); len(got) != 0 {
		t.Errorf("junk produced %d gpus", len(got))
	}
}

func TestParseLSPCI(t *testing.T) {
	out := `00:02.0 "VGA compatible controller" "Intel Corporation" "AlderLake-S GT1" -r0c "Intel Corporation" "Device 00000000"
01:00.0 "VGA compatible controller" "NVIDIA Corporation" "GA104 [GeForce RTX 3070]" -r01 "ASUSTeK Computer Inc." "Device 87c3"
02:00.0 "3D controller" "NVIDIA Corporation" "GA106M [GeForce RTX 3060 Mobile]" -r01 "Dell" "Device 0000"
03:00.0 "Network controller" "Intel Corporation" "Wi-Fi 6 AX200" -r04 "Intel Corporation" "Device 00000000"
`
	models := parseLSPCI(out)
	if len(models) != 3 {
		t.Fatalf("models = %v (%d), want 3", models, len(models))
	}
	if models[0] != "AlderLake-S GT1" {
		t.Errorf("m0 = %q", models[0])
	}
	// shortGPU aligns lspci naming with nvidia-smi style ("GeForce RTX
	// 3070" → "RTX 3070") so the two sources dedup cleanly.
	if models[1] != "RTX 3070" {
		t.Errorf("m1 = %q, want RTX 3070", models[1])
	}
	if models[2] != "RTX 3060 Mobile" {
		t.Errorf("m2 = %q", models[2])
	}
}

func TestParseLSBLK(t *testing.T) {
	out := `nvme0n1 2000398934016 disk 0 Samsung Electronics Co Ltd NVMe SSD Controller SM981a
sda 4000787030016 disk 1 WDC WD40EZRZ-00GXCB0
loop0 123456 loop 0
zram0 99999 disk 0
`
	disks := parseLSBLK(out)
	if len(disks) != 2 {
		t.Fatalf("disks = %+v (%d), want 2", disks, len(disks))
	}
	if disks[0].Model != "Samsung NVMe SSD Controller SM981a" {
		t.Errorf("d0 model = %q", disks[0].Model)
	}
	if disks[0].SizeGB != 1863 {
		t.Errorf("d0 size = %d GB, want 1863", disks[0].SizeGB)
	}
	if disks[0].Rotational {
		t.Errorf("d0 should not be rotational")
	}
	if disks[1].Rotational != true {
		t.Errorf("d1 should be rotational")
	}
	if disks[1].SizeGB != 3726 {
		t.Errorf("d1 size = %d GB, want 3726", disks[1].SizeGB)
	}
}

func TestParseDMIType(t *testing.T) {
	out := `# dmidecode 3.5
Memory Device
	Size: 32 GB
	Type: DDR4
	Type: Unknown
Memory Device
	Size: 32 GB
	Type: DDR4
	Form Factor: DIMM
`
	if got := parseDMIType(out); got != "DDR4" {
		t.Errorf("type = %q, want DDR4", got)
	}
	// All-unknown → empty
	if got := parseDMIType("Type: Unknown\nType: Other\n"); got != "" {
		t.Errorf("unknown type = %q, want empty", got)
	}
}

func TestShortCPU(t *testing.T) {
	cases := map[string]string{
		"AMD Ryzen 9 5900X 12-Core Processor":       "Ryzen 9 5900X",
		"12th Gen Intel(R) Core(TM) i9-12900K":      "Core i9-12900K",
		"Intel(R) Core(TM) i7-13700K CPU @ 3.40GHz": "Core i7-13700K",
		"Intel(R) Core(TM) i5-1240P @ 2.10GHz":      "Core i5-1240P",
		"AMD Ryzen 9 7950X3D 16-Core Processor":     "Ryzen 9 7950X3D",
		"Apple M2 Pro":                              "Apple M2 Pro",
		"AMD Ryzen 5 8600G with Radeon Graphics":    "Ryzen 5 8600G",
		"Intel(R) Celeron(R) CPU  J4105 @ 1.50GHz":  "Celeron",
	}
	for in, want := range cases {
		if got := ShortCPU(in); got != want {
			t.Errorf("ShortCPU(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompactSpecs(t *testing.T) {
	hw := &HardwareSpecs{
		CPU:         "AMD Ryzen 9 5900X 12-Core Processor",
		CPUCores:    24,
		RAMGB:       64,
		RAMType:     "DDR4",
		GPUs:        []GPUInfo{{Model: "RTX 4070", VRAMMB: 12288, Driver: "550.107.02"}},
		Disks:       []DiskInfo{{Model: "Samsung SM981a", SizeGB: 1863}},
		Motherboard: "ASUSTeK ROG STRIX B550-I",
	}
	got := CompactSpecs(hw)
	want := "Ryzen 9 5900X 24c · 64GB DDR4 · RTX 4070 12GB drv 550.107.02 · 1.8T · ASUSTeK ROG STRIX B550-I"
	if got != want {
		t.Errorf("CompactSpecs:\n got  %q\n want %q", got, want)
	}

	// Nil and empty inventories render as ""
	if CompactSpecs(nil) != "" {
		t.Errorf("nil should render empty")
	}
	if CompactSpecs(&HardwareSpecs{}) != "" {
		t.Errorf("empty should render empty")
	}

	// Partial inventory (darwin-ish): no disks, no RAM type
	partial := &HardwareSpecs{CPU: "Apple M2 Pro", CPUCores: 10, RAMGB: 16, Motherboard: "Mac14,6"}
	got = CompactSpecs(partial)
	if got != "Apple M2 Pro 10c · 16GB · Mac14,6" {
		t.Errorf("partial = %q", got)
	}
}

func TestFullSpecsContainsAll(t *testing.T) {
	hw := &HardwareSpecs{
		CPU:      "AMD Ryzen 9 5900X 12-Core Processor",
		CPUCores: 24,
		RAMGB:    64,
		GPUs:     []GPUInfo{{Model: "RTX 4070", VRAMMB: 12288, Driver: "550.107.02"}},
		Disks:    []DiskInfo{{Model: "Samsung SM981a", SizeGB: 1863, Rotational: true}},
	}
	full := FullSpecs(hw)
	for _, want := range []string{"CPU: AMD", "RAM: 64GB", "GPU: RTX 4070 (12288 MiB) driver 550.107.02", "Disk: Samsung SM981a 1.8T (HDD)"} {
		if !strings.Contains(full, want) {
			t.Errorf("FullSpecs missing %q in:\n%s", want, full)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(1024 * 1024 * 1024 * 1024); got != "1.0T" {
		t.Errorf("humanBytes(1TB) = %q", got)
	}
	if got := humanBytes(2 * 1024 * 1024 * 1024 * 1024); got != "2.0T" {
		t.Errorf("humanBytes(2TB) = %q", got)
	}
	if got := humanBytes(500 * 1024 * 1024 * 1024); got != "500G" {
		t.Errorf("humanBytes(500GB) = %q", got)
	}
}
