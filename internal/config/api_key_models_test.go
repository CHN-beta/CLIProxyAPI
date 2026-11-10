package config

import "testing"

func TestParseConfigBytesNormalizesAPIKeyModelPolicies(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
access:
  api-keys:
    - key: key-a
      models: ["  GPT-* ", "gpt-*", ""]
      excluded-models: [" *-PREVIEW ", "*-preview"]
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatalf("APIKeys = %+v", cfg.APIKeys)
	}
	entry := cfg.APIKeys[0]
	if len(entry.Models) != 1 || entry.Models[0] != "gpt-*" {
		t.Fatalf("Models = %#v", entry.Models)
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "*-preview" {
		t.Fatalf("ExcludedModels = %#v", entry.ExcludedModels)
	}
}

func TestLoadConfigNormalizesAPIKeyModelPolicies(t *testing.T) {
	path := writeUploadTestConfig(t, `
access:
  api-keys:
    - key: key-a
      models: [" Gemini-2.5-* ", "gemini-2.5-*"]
      excluded-models: ["*-EXP"]
`)
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatalf("APIKeys = %+v", cfg.APIKeys)
	}
	entry := cfg.APIKeys[0]
	if len(entry.Models) != 1 || entry.Models[0] != "gemini-2.5-*" {
		t.Fatalf("Models = %#v", entry.Models)
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "*-exp" {
		t.Fatalf("ExcludedModels = %#v", entry.ExcludedModels)
	}
}
