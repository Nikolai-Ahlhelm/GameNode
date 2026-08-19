package templates

import (
	"strings"
	"testing"
)

func TestAnalyzeEggContainerRuntime(t *testing.T) {
	template, err := AnalyzeEgg([]byte(`{"name":"container game","startup":"./server --port {{SERVER_PORT}}","docker_images":{"game":"ghcr.io/example/game:1.2.3"},"scripts":{"installation":{"script":"mkdir -p /home/container/data","container":"ghcr.io/example/installer:1","entrypoint":"/bin/sh"}},"variables":[{"env_variable":"SERVER_PORT","default_value":"25565","rules":"required|integer"}],"config":{"files":{"server.properties":{"parser":"properties","replace":{"server-port":"{{SERVER_PORT}}"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if template.ContainerRuntime == nil || template.ContainerRuntime.InstallerImage != "ghcr.io/example/installer:1" {
		t.Fatalf("container plan = %#v", template.ContainerRuntime)
	}
	if template.ContainerCompatibility.Status != Compatible || len(template.ContainerRuntime.ConfigOperations) != 1 {
		t.Fatalf("container compatibility = %#v", template.ContainerCompatibility)
	}
	if template.NativeCompatibility.Status != Unsupported && template.Compatibility.Status != Unsupported {
		t.Fatalf("native compatibility unexpectedly accepted: %#v", template.NativeCompatibility)
	}
	assertContainerFinding(t, template, "CONTAINER_INSTALL_SCRIPT_SANDBOXED")
}

func TestAnalyzeEggContainerRejectsUnsafePlan(t *testing.T) {
	tests := []struct {
		name    string
		egg     string
		finding string
	}{
		{"image", `{"name":"x","startup":"./server","docker_images":{"x":"https://example.invalid/image"},"scripts":{"installation":{"script":"true","container":"https://example.invalid/installer"}}}`, "CONTAINER_IMAGE_INVALID"},
		{"startup", `{"name":"x","startup":"./server $(id)","docker_images":{"x":"ghcr.io/example/game:1"},"scripts":{"installation":{"script":"true"}}}`, "CONTAINER_STARTUP_MALFORMED"},
		{"entrypoint", `{"name":"x","startup":"./server","docker_images":{"x":"ghcr.io/example/game:1"},"scripts":{"installation":{"script":"true","entrypoint":"/bin/zsh"}}}`, "CONTAINER_ENTRYPOINT_UNSUPPORTED"},
		{"config", `{"name":"x","startup":"./server","docker_images":{"x":"ghcr.io/example/game:1"},"config":{"files":{"x":{"parser":"regex","replace":{"a":"b"}}}},"scripts":{"installation":{"script":"true"}}}`, "CONTAINER_CONFIG_SEMANTICS_UNSUPPORTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			template, err := AnalyzeEgg([]byte(test.egg))
			if err != nil {
				t.Fatal(err)
			}
			if template.ContainerCompatibility.Status != Unsupported {
				t.Fatalf("status = %s", template.ContainerCompatibility.Status)
			}
			assertContainerFinding(t, template, test.finding)
		})
	}
	large := strings.Repeat("x", MaxContainerScriptBytes+1)
	if _, err := AnalyzeEgg([]byte(`{"name":"x","startup":"./server","docker_images":{"x":"ghcr.io/example/game:1"},"scripts":{"installation":{"script":"` + large + `"}}}`)); err == nil {
		t.Fatal("oversized container script accepted")
	}
}

func assertContainerFinding(t *testing.T, template Template, code string) {
	t.Helper()
	for _, finding := range template.ContainerCompatibility.Findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("missing container finding %s: %#v", code, template.ContainerCompatibility.Findings)
}
