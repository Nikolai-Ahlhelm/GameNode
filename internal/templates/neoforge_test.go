package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func neoForgeFixture(t *testing.T, platform, launcher string) string {
	t.Helper()
	root := t.TempDir()
	version := "21.1.42"
	name := "unix_args.txt"
	if platform == "windows" {
		name = "win_args.txt"
	}
	relative := filepath.Join("libraries", "net", "neoforged", "neoforge", version, name)
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(relative)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user_jvm_args.txt"), []byte("# local JVM arguments\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, relative), []byte("net.neoforged.fml.startup.Server\n--fml.neoForgeVersion 21.1.42\n--fml.mcVersion 1.21.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcherName := "run.sh"
	if platform == "windows" {
		launcherName = "run.bat"
	}
	if err := os.WriteFile(filepath.Join(root, launcherName), []byte(launcher), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveNeoForgeWindowsAndLinux(t *testing.T) {
	for _, test := range []struct{ platform, launcher, suffix string }{
		{"windows", "@echo off\r\nREM generated\r\njava @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt %*\r\npause\r\n", "win_args.txt"},
		{"linux", "#!/usr/bin/env sh\n# generated\nexec java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/unix_args.txt \"$@\"\n", "unix_args.txt"},
	} {
		t.Run(test.platform, func(t *testing.T) {
			root := neoForgeFixture(t, test.platform, test.launcher)
			resolved, err := ResolveNeoForge(root, test.platform, 1024, 4096, true)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.NeoForgeVersion != "21.1.42" || resolved.MinecraftVersion != "1.21.1" || resolved.WorkingDirectory != root {
				t.Fatalf("unexpected resolution: %+v", resolved)
			}
			joined := strings.Join(resolved.Arguments, " ")
			if !strings.Contains(joined, "-Xms1024M -Xmx4096M") || !strings.Contains(joined, test.suffix) || !strings.HasSuffix(joined, "nogui") {
				t.Fatalf("arguments = %q", joined)
			}
		})
	}
}

func TestResolveNeoForgeRejectsUnsafeLaunchers(t *testing.T) {
	cases := []string{
		"java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt && calc.exe\n",
		"cmd.exe /c java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt\n",
		"java @user_jvm_args.txt @../../outside/win_args.txt\n",
		"java @user_jvm_args.txt @C:/outside/win_args.txt\n",
		"java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt\nwhoami\n",
	}
	for _, launcher := range cases {
		root := neoForgeFixture(t, "windows", launcher)
		if _, err := ResolveNeoForge(root, "windows", 1024, 4096, true); err == nil {
			t.Fatalf("unsafe launcher accepted: %q", launcher)
		}
	}
}

func TestResolveNeoForgeMissingFilesAndMemory(t *testing.T) {
	root := neoForgeFixture(t, "windows", "java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt %*\n")
	if err := os.Remove(filepath.Join(root, "user_jvm_args.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNeoForge(root, "windows", 1024, 4096, true); err == nil {
		t.Fatal("missing JVM argfile accepted")
	}
	root = neoForgeFixture(t, "windows", "java @user_jvm_args.txt @libraries/net/neoforged/neoforge/21.1.42/win_args.txt %*\n")
	if _, err := ResolveNeoForge(root, "windows", 4096, 1024, true); err == nil {
		t.Fatal("invalid memory range accepted")
	}
}

func TestLocalNeoForgeReferenceWhenPresent(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "server-test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); os.IsNotExist(err) {
		t.Skip("local NeoForge reference server is not present")
	}
	resolved, err := ResolveNeoForge(root, "windows", 1024, 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.NeoForgeVersion != "26.2.0.59" || resolved.MinecraftVersion != "26.2" || !strings.HasSuffix(resolved.Arguments[2], "win_args.txt") {
		t.Fatalf("unexpected local reference resolution: %+v", resolved)
	}
}
