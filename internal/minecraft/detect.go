package minecraft

import "path/filepath"

import "os"

// DetectLoader reports the mod loader an installed server uses ("neoforge",
// "forge", "fabric") from the files each installer leaves behind, or "" when it
// cannot tell (vanilla, or an unrecognized layout). It is a best-effort hint for
// the UI, never a security or launch decision.
func DetectLoader(root string) string {
	for _, candidate := range []struct{ relative, loader string }{
		{filepath.Join("libraries", "net", "neoforged", "neoforge"), LoaderNeoForge},
		{filepath.Join("libraries", "net", "minecraftforge", "forge"), LoaderForge},
		{".fabric", LoaderFabric},
		{"fabric-server-launcher.properties", LoaderFabric},
	} {
		if _, err := os.Stat(filepath.Join(root, candidate.relative)); err == nil {
			return candidate.loader
		}
	}
	return ""
}
