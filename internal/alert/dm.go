// Manual-step surfacing (BCR-19): when a recipe blocks on sudo, the fleet
// needs Benn's hands — a Discord DM reaches his phone even when nothing
// else is being watched. This is DETECT + NOTIFY only: no privilege
// grants, no remote command execution, ever.
//
// Transport: Discord bot API (a webhook cannot DM a user — webhooks post
// into a channel only). The bot token and DM channel id are resolved at
// runtime from a NetEnv secret, never stored in the repo or agent.env.

package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/bennbanks/gogitops/internal/agentlog"
)

// dmMessageLimit is Discord's hard content cap (2000) minus headroom for
// the prefix the apply loop adds (hostname + recipe name).
const dmMessageLimit = 1900

// DMSender posts direct messages through the Discord bot API.
type DMSender struct {
	token   string
	channel string
	client  *http.Client
	apiBase string // overridable for tests; default https://discord.com/api/v10
}

// NewDMSender creates a DM sender from a ref:
//   - "nenv:<namespace>/<key>" — resolved via nenv CLI at runtime
//   - a raw "<channel_id>|<bot_token>" value
//   - "" (or unresolvable/unparseable) → an unconfigured sender whose Send
//     is a silent no-op, mirroring the webhook's "empty = no alerts".
//
// The secret value format is "<channel_id>|<bot_token>" — one entry keeps
// the flag surface identical to -webhook (single nenv ref).
func NewDMSender(ref string) *DMSender {
	s := &DMSender{client: &http.Client{Timeout: 10 * time.Second}, apiBase: "https://discord.com/api/v10"}
	value := ref
	if strings.HasPrefix(ref, "nenv:") {
		value = resolveNenv(ref[5:])
		if value == ref[5:] {
			// resolution failed — stay unconfigured rather than send
			// credentials to a wrong destination
			return s
		}
	}
	channel, token, ok := parseDMValue(value)
	if !ok {
		if strings.TrimSpace(ref) != "" {
			agentlog.Default().Warnf("alert", "dm-notify: unparseable config (want nenv:<ns>/<key> holding <channel_id>|<bot_token>) — DMs disabled")
		}
		return s
	}
	s.channel, s.token = channel, token
	return s
}

// Configured reports whether a DM can actually be sent.
func (s *DMSender) Configured() bool {
	return s != nil && s.token != "" && s.channel != ""
}

// resolveNenv runs `nenv get <ns> <key>` and returns the trimmed value, or
// "" on any failure (the caller decides whether that is fatal).
func resolveNenv(ref string) string {
	ns, key := splitNenvRef(ref)
	out, err := exec.Command(NenvBin(), "get", ns, key).Output()
	if err != nil {
		return ""
	}
	return trimNewline(string(out))
}

// parseDMValue splits "<channel_id>|<bot_token>". Returns ok=false when the
// shape is wrong (missing separator, non-numeric channel, empty token).
func parseDMValue(v string) (channel, token string, ok bool) {
	i := strings.Index(v, "|")
	if i <= 0 || i == len(v)-1 {
		return "", "", false
	}
	channel, token = strings.TrimSpace(v[:i]), strings.TrimSpace(v[i+1:])
	if channel == "" || token == "" {
		return "", "", false
	}
	for _, r := range channel {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return channel, token, true
}

// Send posts a plain content message to the configured DM channel.
// The User-Agent header is mandatory — Discord's CDN rejects UA-less
// requests (Cloudflare 1010).
func (s *DMSender) Send(message string) error {
	if !s.Configured() {
		return fmt.Errorf("dm sender not configured")
	}
	if len(message) > dmMessageLimit {
		message = message[:dmMessageLimit] + "…"
	}
	body, _ := json.Marshal(map[string]string{"content": message})
	url := strings.TrimSuffix(s.apiBase, "/") + "/channels/" + s.channel + "/messages"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gogitops-agent/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned %d", resp.StatusCode)
	}
	return nil
}
