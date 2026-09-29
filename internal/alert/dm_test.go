package alert

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDMValue(t *testing.T) {
	cases := []struct {
		in                string
		channel, token    string
		ok                bool
	}{
		{"1500019887295303730|MTIzNDU2Nzg5.gTOK", "1500019887295303730", "MTIzNDU2Nzg5.gTOK", true},
		{" 1234| abcdef ", "1234", "abcdef", true},
		{"no-separator", "", "", false},
		{"|token-only", "", "", false},          // empty channel
		{"chan|", "", "", false},                // empty token
		{"12a34|token", "", "", false},          // non-numeric channel
		{"", "", "", false},
	}
	for _, c := range cases {
		ch, tok, ok := parseDMValue(c.in)
		if ok != c.ok || (ok && (ch != c.channel || tok != c.token)) {
			t.Errorf("parseDMValue(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, ch, tok, ok, c.channel, c.token, c.ok)
		}
	}
}

func TestNewDMSenderRawValue(t *testing.T) {
	s := NewDMSender("123456789012345678|tok")
	if !s.Configured() {
		t.Fatal("raw channel|token must configure the sender")
	}
	if s.channel != "123456789012345678" || s.token != "tok" {
		t.Errorf("parse mismatch: %q %q", s.channel, s.token)
	}
	// empty ref → unconfigured, Send is a safe error
	s2 := NewDMSender("")
	if s2.Configured() {
		t.Error("empty ref must stay unconfigured")
	}
	if err := s2.Send("x"); err == nil {
		t.Error("unconfigured Send must error, not panic")
	}
}

func TestDMSendHTTP(t *testing.T) {
	var gotAuth, gotUA, gotPath string
	var gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var payload map[string]string
		json.Unmarshal(body, &payload)
		gotContent = payload["content"]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := NewDMSender("1500019887295303730|fake-token")
	s.apiBase = srv.URL
	if err := s.Send("🔧 gogitops [mini] recipe blocked on sudo — needs your hands:\ninternal-dns: sudo nmcli con mod X"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "Bot fake-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotUA == "" {
		t.Error("User-Agent header is mandatory (Discord CDN 403s without one)")
	}
	if gotPath != "/channels/1500019887295303730/messages" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotContent, "blocked on sudo") {
		t.Errorf("content = %q", gotContent)
	}
}

func TestDMSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()
	s := NewDMSender("123|tok")
	s.apiBase = srv.URL
	if err := s.Send("msg"); err == nil {
		t.Error("non-2xx must return an error")
	}
}

func TestDMMessageLimit(t *testing.T) {
	// the truncation lives inside Send; assert the constant keeps us under Discord's 2000 cap
	if dmMessageLimit >= 2000 {
		t.Errorf("dmMessageLimit %d must stay under Discord's 2000 cap", dmMessageLimit)
	}
}
