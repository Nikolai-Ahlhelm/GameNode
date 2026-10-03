package minecraft

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// TestLiveInstallAndResolve installs each loader from the real upstream
// sources and resolves its launch. It needs network access and Java and is
// opt-in: GAMENODE_MINECRAFT_LIVE=1 go test ./internal/minecraft -run Live -v
func TestLiveInstallAndResolve(t *testing.T) {
	if os.Getenv("GAMENODE_MINECRAFT_LIVE") != "1" {
		t.Skip("set GAMENODE_MINECRAFT_LIVE=1 to run against the real upstream sources")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	source := NewSource()
	installer := NewInstaller(source)
	const game = "1.21.1"
	for _, loader := range Loaders {
		t.Run(loader, func(t *testing.T) {
			plan := Plan{Loader: loader, MinecraftVersion: game}
			if loader != LoaderVanilla {
				builds, err := source.LoaderVersions(ctx, loader, game)
				if err != nil || len(builds) == 0 {
					t.Fatalf("builds: %v %v", builds, err)
				}
				for _, build := range builds {
					if build.Latest {
						plan.LoaderVersion = build.Version
					}
				}
			}
			root := t.TempDir()
			if err := installer.Install(ctx, root, plan, io.Discard, func(e Event) { t.Log(e.Phase, e.Summary) }); err != nil {
				t.Fatalf("install %+v: %v", plan, err)
			}
			launch, err := ResolveLaunch(root, "", plan, 1024, 2048, true)
			if err != nil {
				t.Fatalf("resolve %+v: %v", plan, err)
			}
			t.Logf("%s %v", launch.Executable, launch.Arguments)
		})
	}
}
