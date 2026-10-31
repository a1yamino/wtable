package server

import (
	"regexp"
	"strings"
	"testing"

	"wtable/internal/model"
)

// Task text reaches a terminal verbatim on the bracketed-paste and steer
// paths. An embedded ESC[201~ would terminate the paste and turn the remaining
// envelope into keystrokes; CR would submit extra lines.
func TestDeliveryPayloadNeutralisesTerminalControls(t *testing.T) {
	task := model.Task{ID: "t-1", Prompt: "ok\x1b[201~\rrm -rf /\x03\u009b31m\nnext\ttab"}
	for _, tc := range []struct {
		name      string
		mode      string
		bracketed bool
	}{
		{"bracketed-paste", "push", true},
		{"single-line-json", "push", false},
		{"steer", "steer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, data := deliveryPayload(task, tc.mode, "t-0", tc.bracketed)
			if strings.Contains(envelope, "\x1b") || strings.Contains(envelope, "\r") || strings.Contains(envelope, "\x03") {
				t.Fatalf("envelope kept terminal controls: %q", envelope)
			}
			if !strings.Contains(envelope, "␛") || !strings.Contains(envelope, "␍") || !strings.Contains(envelope, "␃") {
				t.Fatalf("control characters are not visible in the envelope: %q", envelope)
			}
			if strings.Contains(data, "\x03") {
				t.Fatalf("payload kept a control byte: %q", data)
			}
			switch {
			case tc.mode == "steer":
				if strings.ContainsAny(data, "\x1b\n") {
					t.Fatalf("steer payload is not one plain line: %q", data)
				}
				if strings.Count(data, "\r") != 1 || !strings.HasSuffix(data, "\r") {
					t.Fatalf("steer payload must end with exactly one submit: %q", data)
				}
			case tc.bracketed:
				if strings.Count(data, "\x1b[201~") != 1 || !strings.HasSuffix(data, "\x1b[201~\r") {
					t.Fatalf("prompt can terminate bracketed paste early: %q", data)
				}
				if strings.Count(data, "\x1b") != 2 {
					t.Fatalf("unexpected escape sequences in paste payload: %q", data)
				}
			default:
				if strings.Contains(data, "\x1b") || strings.Count(data, "\r") != 1 || !strings.HasSuffix(data, "\r") {
					t.Fatalf("JSON payload kept raw controls: %q", data)
				}
			}
		})
	}
}

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
