package server

import (
	"regexp"
	"testing"

	"wtable/internal/model"
)

func TestDefaultBlockedPrompts(t *testing.T) {
	prompts := []string{
		"Would you like to run the following command?",
		"Run this command?",
		"Do you trust the contents of this project?",
		"Allow access to this file?",
		"You are currently not signed in.",
	}
	for _, command := range []string{"codex", "/usr/local/bin/agy", "claude"} {
		t.Run(command, func(t *testing.T) {
			config := deliveryConfig(&model.Pane{Command: []string{command}})
			re, err := regexp.Compile(config.BlockedRegex)
			if err != nil {
				t.Fatal(err)
			}
			for _, prompt := range prompts {
				if !re.MatchString(prompt) {
					t.Errorf("missed blocking prompt %q", prompt)
				}
			}
			for _, output := range []string{
				"permissions := map[string]bool{}",
				"Checking file permissions before writing output",
				"The command runner uses the project contents",
				"Access to this file was granted",
			} {
				if re.MatchString(output) {
					t.Errorf("blocked ordinary output %q", output)
				}
			}
		})
	}
	if config := deliveryConfig(&model.Pane{Command: []string{"/bin/sh", "-c", "codex"}}); config.BlockedRegex != "" {
		t.Fatal("preset must apply only to direct agent commands")
	}
	custom := "custom login gate"
	if config := deliveryConfig(&model.Pane{Command: []string{"codex"}, DeliveryConfig: model.DeliveryConfig{BlockedRegex: custom}}); config.BlockedRegex != custom {
		t.Fatal("preset replaced explicit regex")
	}
}
