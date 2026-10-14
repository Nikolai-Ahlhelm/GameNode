package minecraft

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestResolveConfiguredLaunchDiscoversNeoForgeAndBoundsMemory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "libraries", "net", "neoforged", "neoforge", "21.1.256"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "libraries", "net", "neoforged", "neoforge", "21.1.9"), 0o755); err != nil {
		t.Fatal(err)
	}
	argfile := filepath.Join(root, "libraries", "net", "neoforged", "neoforge", "21.1.256", "unix_args.txt")
	if runtime.GOOS == "windows" {
		argfile = filepath.Join(filepath.Dir(argfile), "win_args.txt")
	}
	if err := os.WriteFile(argfile, []byte("-cp libraries/example.jar net.minecraft.server.Main"), 0o600); err != nil {
		t.Fatal(err)
	}
	olderArgfile := filepath.Join(root, "libraries", "net", "neoforged", "neoforge", "21.1.9", filepath.Base(argfile))
	if err := os.WriteFile(olderArgfile, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user_jvm_args.txt"), []byte("-Xms2G -Xmx6G -XX:+UseG1GC -Dcustom=kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	javaName := "java"
	if runtime.GOOS == "windows" {
		javaName += ".exe"
	}
	java := filepath.Join(root, javaName)
	if err := os.WriteFile(java, []byte("java"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := StartupConfiguration{Distribution: "neoforge", JavaExecutable: java, XmsMB: 4096, XmxMB: 12288, JVMArguments: []string{"-XX:+UseG1GC"}, ProgramArguments: []string{"nogui"}}
	launch, err := ResolveConfiguredLaunch(root, runtime.GOOS, config, 16<<30)
	if err != nil {
		t.Fatal(err)
	}
	if len(launch.Arguments) != 6 || launch.Arguments[0] != "-Xms4096M" || launch.Arguments[1] != "-Xmx12288M" || launch.Arguments[2] != "-XX:+UseG1GC" || launch.Arguments[3] != "-Dcustom=kept" {
		t.Fatalf("unexpected launch args: %#v", launch.Arguments)
	}
	if launch.Arguments[len(launch.Arguments)-2] != "@libraries/net/neoforged/neoforge/21.1.256/"+filepath.Base(argfile) || launch.Arguments[len(launch.Arguments)-1] != "nogui" {
		t.Fatalf("argfile/program args not retained: %#v", launch.Arguments)
	}
	if _, err = ResolveConfiguredLaunch(root, runtime.GOOS, config, 8<<30); err == nil {
		t.Fatal("expected container memory limit rejection")
	}
}

func TestImportJVMArgumentsExtractsHeapAndPreservesOtherArguments(t *testing.T) {
	got, err := ImportJVMArguments([]byte("# memory\n-Xms4G -Xmx12G\n-XX:+UseG1GC -Dexample=hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.XmsMB != 4096 || got.XmxMB != 12288 {
		t.Fatalf("heap values: %d/%d", got.XmsMB, got.XmxMB)
	}
	want := []string{"-XX:+UseG1GC", "-Dexample=hello"}
	if !reflect.DeepEqual(got.JVMArguments, want) {
		t.Fatalf("custom args = %#v, want %#v", got.JVMArguments, want)
	}
	if _, err = ResolveConfiguredLaunch(t.TempDir(), runtime.GOOS, StartupConfiguration{Distribution: "vanilla", JavaExecutable: "java", XmsMB: 1024, XmxMB: 2048, JVMArguments: []string{"-Xmx8G"}}, 0); err == nil {
		t.Fatal("duplicate managed Xmx should be rejected")
	}
}

func TestImportRejectsInvalidHeapAndUnsafeArguments(t *testing.T) {
	for _, input := range []string{"-Xmx0G", "-Xms8G -Xmx4G", "-Xms999999999999G", "-Xmx4G 'unclosed"} {
		if _, err := ImportJVMArguments([]byte(input)); err == nil {
			t.Errorf("expected %q to be rejected", input)
		}
	}
}
