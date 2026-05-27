package app

import (
	"path/filepath"
	"testing"

	"napcat-file-mover/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	base := t.TempDir()
	paths := config.Paths{
		BaseDir:  base,
		Config:   filepath.Join(base, "config.yaml"),
		Database: filepath.Join(base, "mover.db"),
		CacheDir: filepath.Join(base, "cache"),
		FilesDir: filepath.Join(base, "files"),
		LogDir:   filepath.Join(base, "logs"),
	}
	cfg := config.Default(paths)
	cfg.Storage.S3.SecretKey = "old-secret"
	cfg.NapCat.Token = "old-token"
	cfg.App.AdminToken = "old-admin"
	return cfg
}

func TestPublicConfigRedactsSecrets(t *testing.T) {
	core := &Core{cfg: testConfig(t)}
	pub := core.publicConfig()
	if pub.NapCat.Token != "" {
		t.Fatal("napcat token should be redacted")
	}
	if pub.App.AdminToken != "" {
		t.Fatal("admin token should be redacted")
	}
	if pub.Storage.S3.SecretKey != "" {
		t.Fatal("s3 secret should be redacted")
	}
	if !pub.Storage.S3.SecretKeySet {
		t.Fatal("s3 secret set flag should be true")
	}
}

func TestUpdatedConfigPreservesBlankSecrets(t *testing.T) {
	core := &Core{cfg: testConfig(t)}
	req := core.publicConfig()
	req.NapCat.Token = ""
	req.App.AdminToken = ""
	req.Storage.S3.SecretKey = ""

	next, err := core.updatedConfig(req)
	if err != nil {
		t.Fatalf("updatedConfig: %v", err)
	}
	if next.NapCat.Token != "old-token" {
		t.Fatalf("napcat token was not preserved: %q", next.NapCat.Token)
	}
	if next.App.AdminToken != "old-admin" {
		t.Fatalf("admin token was not preserved: %q", next.App.AdminToken)
	}
	if next.Storage.S3.SecretKey != "old-secret" {
		t.Fatalf("s3 secret was not preserved: %q", next.Storage.S3.SecretKey)
	}
}

func TestImmutableChanged(t *testing.T) {
	a := testConfig(t)
	b := *a
	if immutableChanged(a, &b) {
		t.Fatal("identical config should not require restart")
	}
	b.Bot.AllowedGroups = []int64{42}
	if immutableChanged(a, &b) {
		t.Fatal("allowlist change should be hot reloadable")
	}
	b = *a
	b.Database.Driver = "postgres"
	if !immutableChanged(a, &b) {
		t.Fatal("database backend change should require restart")
	}
	b = *a
	b.Storage.Type = "s3"
	if !immutableChanged(a, &b) {
		t.Fatal("storage backend type change should require restart")
	}
}
