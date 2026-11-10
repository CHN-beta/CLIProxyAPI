package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildConfigChangeDetailsDetectsAPIKeyModelPolicyChanges(t *testing.T) {
	base := &config.Config{}
	base.APIKeys = []config.APIKeyEntry{{Key: "key-a", Models: []string{"gpt-*"}}}

	excluded := &config.Config{}
	excluded.APIKeys = []config.APIKeyEntry{{Key: "key-a", Models: []string{"gpt-*"}, ExcludedModels: []string{"*-preview"}}}

	changes := BuildConfigChangeDetails(base, excluded)
	expectContains(t, changes, "api-keys: values updated (count unchanged, redacted)")

	allowlist := &config.Config{}
	allowlist.APIKeys = []config.APIKeyEntry{{Key: "key-a", Models: []string{"gemini-*"}}}
	changes = BuildConfigChangeDetails(excluded, allowlist)
	expectContains(t, changes, "api-keys: values updated (count unchanged, redacted)")

	// Normalization makes formatting-only differences invisible to the watcher.
	normalized := &config.Config{}
	normalized.APIKeys = []config.APIKeyEntry{{Key: "key-a", Models: []string{"  GPT-* ", "gpt-*"}}}
	if changes := BuildConfigChangeDetails(base, normalized); len(changes) != 0 {
		t.Fatalf("normalized-identical policy produced changes: %v", changes)
	}
}
