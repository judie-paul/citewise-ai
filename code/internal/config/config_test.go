package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearEnv(t *testing.T) {
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_MODEL", "PORT", "DATABASE_PATH", "MAX_UPLOAD_MB", "TEMP_DIR", "CROSSREF_MAILTO", "OPENALEX_API_KEY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.DatabasePath != "./data/citewise.db" || cfg.MaxUploadMB != 10 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.AIEnabled() {
		t.Fatal("AI should be disabled without a key")
	}
	if cfg.OpenAIModel != DefaultOpenAIModel {
		t.Fatalf("model = %q", cfg.OpenAIModel)
	}
}

func TestLoadInvalid(t *testing.T) {
	clearEnv(t)
	t.Setenv("MAX_UPLOAD_MB", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid MAX_UPLOAD_MB")
	}
	clearEnv(t)
	t.Setenv("PORT", "99999")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid PORT")
	}
}

func TestLoadDotEnvDoesNotOverride(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nPORT=9090\nOPENAI_MODEL=\"custom-model\"\nCROSSREF_MAILTO=a@b.c\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CROSSREF_MAILTO", "existing@x.y")
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "9090" || cfg.OpenAIModel != "custom-model" {
		t.Fatalf("dotenv not applied: %+v", cfg)
	}
	if cfg.CrossrefMailto != "existing@x.y" {
		t.Fatalf("existing env overwritten: %q", cfg.CrossrefMailto)
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
}
