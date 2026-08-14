// Package alert sends notifications to Discord webhooks.
package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"
)

// Sender sends alerts via Discord webhook
type Sender struct {
	webhookURL string
	client     *http.Client
}

// NewSender creates an alert sender. webhookURL can be:
//   - a direct URL
//   - "nenv:<namespace>/<key>" — resolved via nenv CLI at runtime
func NewSender(webhookURL string) *Sender {
	return &Sender{
		webhookURL: resolveWebhook(webhookURL),
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// resolveWebhook handles the nenv: prefix
func resolveWebhook(url string) string {
	if len(url) > 5 && url[:5] == "nenv:" {
		ref := url[5:] // e.g. "ctl/ALERT_DISCORD_WEBHOOK"
		ns, key := splitNenvRef(ref)
		out, err := exec.Command("nenv", "get", ns, key).Output()
		if err == nil {
			resolved := trimNewline(string(out))
			if resolved != "" {
				return resolved
			}
		}
	}
	return url
}

func splitNenvRef(ref string) (string, string) {
	// "ctl/ALERT_DISCORD_WEBHOOK" → ("ctl", "ALERT_DISCORD_WEBHOOK")
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:]
		}
	}
	return "global", ref
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// Send posts a message to Discord
func (s *Sender) Send(message string) error {
	if s.webhookURL == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	payload := map[string]interface{}{
		"content": message,
	}
	body, _ := json.Marshal(payload)
	resp, err := s.client.Post(s.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned %d", resp.StatusCode)
	}
	return nil
}

// SendAlert sends a formatted alert with severity emoji
func (s *Sender) SendAlert(severity, hostname, text string) error {
	var emoji string
	switch severity {
	case "critical":
		emoji = "🚨"
	case "warning":
		emoji = "⚠️"
	case "info":
		emoji = "ℹ️"
	default:
		emoji = "🔔"
	}
	msg := fmt.Sprintf("%s **%s**: %s", emoji, hostname, text)
	return s.Send(msg)
}
