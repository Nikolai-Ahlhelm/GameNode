package templates

import "testing"

func TestSafeStartupExtraction(t *testing.T) {
	known := map[string]bool{"PORT": true}
	launch, findings := analyzeStartup(`./server --name "Seven Days" --port=${PORT}`, known)
	if launch == nil || launch.Executable != "./server" || len(launch.Arguments) != 3 || len(findings) != 0 {
		t.Fatalf("launch=%#v findings=%#v", launch, findings)
	}
}

func TestStartupShellConstructsAreNeverImported(t *testing.T) {
	for _, input := range []string{`./server && evil`, `./server || evil`, `./server; evil`, `./server | evil`, `./server > out`, `./server < in`, `./server & evil`} {
		launch, findings := analyzeStartup(input, nil)
		if launch == nil || len(findings) == 0 || findings[0].Code != "UNSUPPORTED_SHELL_STARTUP" {
			t.Fatalf("%q launch=%#v findings=%#v", input, launch, findings)
		}
	}
	for _, input := range []string{`$(evil)`, `./server "$(evil)"`, `./server ` + "`evil`", `bash -c ./server`, `sh ./start`, `C:\\server\\game.exe`, `../server`, `./server --config=../../outside`} {
		launch, _ := analyzeStartup(input, nil)
		if launch != nil {
			t.Fatalf("unsafe startup accepted: %q", input)
		}
	}
}

func TestContainerStartupPathIsMapped(t *testing.T) {
	launch, findings := analyzeStartup(`/home/container/server --root=/mnt/server/data`, nil)
	if launch == nil || launch.Executable != "./server" || launch.Arguments[0] != "--root=./data" {
		t.Fatalf("launch=%#v", launch)
	}
	found := false
	for _, finding := range findings {
		found = found || finding.Code == "CONTAINER_PATH_MAPPED"
	}
	if !found {
		t.Fatal("mapping finding missing")
	}
}

func TestQuotedOperatorsAreArguments(t *testing.T) {
	launch, findings := analyzeStartup(`./server --motd "safe && literal"`, nil)
	if launch == nil || len(findings) != 0 || launch.Arguments[1] != "safe && literal" {
		t.Fatalf("launch=%#v findings=%#v", launch, findings)
	}
}
