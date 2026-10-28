// Package config loads application settings from the environment and an
// optional .env file.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultOpenAIModel is used when OPENAI_MODEL is empty.
const DefaultOpenAIModel = "gpt-4o-mini"

// Config holds all runtime settings.
type Config struct {
	OpenAIKey      string
	OpenAIModel    string
	Port           string
	DatabasePath   string
	MaxUploadMB    int
	TempDir        string
	CrossrefMailto string
	OpenAlexKey    string
}

// AIEnabled reports whether an OpenAI key is configured.
func (c Config) AIEnabled() bool { return c.OpenAIKey != "" }

// MaxUploadBytes returns the upload limit in bytes.
func (c Config) MaxUploadBytes() int64 { return int64(c.MaxUploadMB) << 20 }

// Load reads configuration from the process environment. Missing values fall
// back to defaults; invalid values return an error.
func Load() (Config, error) {
	cfg := Config{
		OpenAIKey:      strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIModel:    strings.TrimSpace(os.Getenv("OPENAI_MODEL")),
		Port:           strings.TrimSpace(os.Getenv("PORT")),
		DatabasePath:   strings.TrimSpace(os.Getenv("DATABASE_PATH")),
		TempDir:        strings.TrimSpace(os.Getenv("TEMP_DIR")),
		CrossrefMailto: strings.TrimSpace(os.Getenv("CROSSREF_MAILTO")),
		OpenAlexKey:    strings.TrimSpace(os.Getenv("OPENALEX_API_KEY")),
		MaxUploadMB:    10,
	}
	if cfg.OpenAIModel == "" {
		cfg.OpenAIModel = DefaultOpenAIModel
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if p, err := strconv.Atoi(cfg.Port); err != nil || p < 1 || p > 65535 {
		return cfg, fmt.Errorf("PORT must be a number between 1 and 65535, got %q", cfg.Port)
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "./data/citewise.db"
	}
	if v := strings.TrimSpace(os.Getenv("MAX_UPLOAD_MB")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return cfg, fmt.Errorf("MAX_UPLOAD_MB must be between 1 and 100, got %q", v)
		}
		cfg.MaxUploadMB = n
	}
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	return cfg, nil
}

// LoadDotEnv reads KEY=VALUE lines from path into the environment. Variables
// that are already set are not overwritten. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists && key != "" {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}
