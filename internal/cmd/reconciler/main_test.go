package reconciler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoesNotLoadDotenv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for key, value := range map[string]string{
		"NAIS_API_URL": "", "GITHUB_ORG": "example", "GITHUB_APP_ID": "123", "GITHUB_APP_PRIVATE_KEY_FILE": "/keys/app.pem",
		"SYNC_INTERVAL": "30m", "SYNC_TIMEOUT": "30m", "TEAM_TIMEOUT": "2m",
		"DRY_RUN": "false",
	} {
		t.Setenv(key, value)
	}
	// An invalid local dotenv file must not affect configuration of the binary.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("=invalid dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "configuration:") || !strings.Contains(err.Error(), "NAIS_API_URL") {
		t.Fatalf("expected missing process environment configuration, got %v", err)
	}
}
