package servers

import "testing"

func TestContainerConfigRejectsUnsafeOrUnboundedInput(t *testing.T) {
	valid := ContainerConfig{Image: "ghcr.io/example/game:1.0", Command: []string{"./server"}, MemoryLimitBytes: 64 << 20, CPULimitMillis: 1000}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for name, mutate := range map[string]func(*ContainerConfig){
		"url":         func(c *ContainerConfig) { c.Image = "https://example.invalid/image" },
		"digest":      func(c *ContainerConfig) { c.ImageDigest = "sha256:not-a-digest" },
		"memory":      func(c *ContainerConfig) { c.MemoryLimitBytes = 1 },
		"cpu":         func(c *ContainerConfig) { c.CPULimitMillis = 0 },
		"nul command": func(c *ContainerConfig) { c.Command = []string{"bad\x00value"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}
