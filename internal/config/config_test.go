package config

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backend.Type != "wcf" {
		t.Fatalf("unexpected backend: %q", cfg.Backend.Type)
	}
	if len(cfg.Network.HTTPServers) != 1 || cfg.Network.HTTPServers[0].Port != 3000 {
		t.Fatalf("unexpected http servers: %#v", cfg.Network.HTTPServers)
	}
	if len(cfg.Network.WebsocketServers) != 1 || cfg.Network.WebsocketServers[0].Port != 3001 {
		t.Fatalf("unexpected ws servers: %#v", cfg.Network.WebsocketServers)
	}
	if cfg.IDMode != "numeric" || cfg.GroupMode != "at" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("WXCAT_BACKEND", "mimicwx")
	t.Setenv("WXCAT_WCF_CMD_ADDR", "tcp://backend:11086")
	t.Setenv("WXCAT_WCF_MSG_ADDR", "tcp://backend:11087")
	t.Setenv("WXCAT_WCF_MEDIA_URL", "http://backend:11090")
	t.Setenv("WXCAT_WCF_MEDIA_TOKEN", "secret")

	cfg := Default()
	cfg.ApplyEnv()

	if cfg.Backend.Type != "mimicwx" {
		t.Fatalf("backend not overridden: %q", cfg.Backend.Type)
	}
	if cfg.Backend.WCF.CmdAddr != "tcp://backend:11086" ||
		cfg.Backend.WCF.MsgAddr != "tcp://backend:11087" ||
		cfg.Backend.WCF.MediaURL != "http://backend:11090" ||
		cfg.Backend.WCF.MediaToken != "secret" {
		t.Fatalf("wcf addresses not overridden: %#v", cfg.Backend.WCF)
	}
}

func TestSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wxcat.json")
	cfg := Default()
	cfg.Network.HTTPServers[0].Token = "abc"
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Network.HTTPServers[0].Token != "abc" {
		t.Fatalf("token lost: %#v", loaded.Network.HTTPServers[0])
	}
}
