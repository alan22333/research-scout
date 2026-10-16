package config

import "testing"

func TestLoadDefaultsToLocalAtomicMode(t *testing.T) {
	t.Setenv("INDEX_EPOCH_PUBLISH_MODE", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.IndexEpochPublishMode != "atomic" || c.AppEnv != "local" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}
