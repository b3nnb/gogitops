package config

import "testing"

func TestPrintDetectedForOps(t *testing.T) {
	t.Logf("DETECT lan=%q nebula=%q machine-id=%q identity-macs=%v portable-macs=%v",
		DetectLanIP(), DetectNebulaIP(), DetectMachineID(), DetectNICs().Macs, DetectNICs().Portable)
}