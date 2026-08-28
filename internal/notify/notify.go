// Package notify provides best-effort desktop notifications across Linux and macOS.
package notify

import (
	"os/exec"
	"runtime"
)

// Send posts a non-blocking desktop notification. Errors are silently ignored.
func Send(title, message string) {
	if title == "" {
		title = "agy-rotator"
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := "display notification \"" + escapeQuotes(message) + "\" with title \"" + escapeQuotes(title) + "\""
		cmd = exec.Command("osascript", "-e", script)
	default:
		// Linux / BSD: notify-send
		cmd = exec.Command("notify-send", title, message)
	}
	_ = cmd.Start()
}

func escapeQuotes(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return string(b)
}
