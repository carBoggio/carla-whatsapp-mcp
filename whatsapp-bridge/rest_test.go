package main

import (
	"bytes"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// The REST guards run before any WhatsApp call, so they can be tested without a client.
func TestRESTGuards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")
	writeFile(t, path, `{"send":["34600111222"],"read":["34600111222"]}`)
	allow = NewAllowlist(path)
	audit = &AuditLog{path: filepath.Join(dir, "audit.log")}

	startRESTServer(nil, nil, 18080)
	time.Sleep(200 * time.Millisecond)

	post := func(url, body string) int {
		resp, err := http.Post("http://127.0.0.1:18080"+url, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("/api/send", `{"recipient":"34699999999","message":"hola"}`); c != 403 {
		t.Errorf("unlisted recipient: status %d, want 403", c)
	}
	if c := post("/api/send", `{"recipient":"120363012345678901@g.us","message":"hola"}`); c != 403 {
		t.Errorf("unlisted group: status %d, want 403", c)
	}
	if c := post("/api/send", `{"recipient":"34600111222","media_path":"/etc/passwd"}`); c != 403 {
		t.Errorf("media send: status %d, want 403", c)
	}
	if c := post("/api/download", `{"message_id":"x","chat_jid":"34699999999@s.whatsapp.net"}`); c != 403 {
		t.Errorf("download from unlisted chat: status %d, want 403", c)
	}

	// must not listen on non-loopback interfaces
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ip, _, _ := net.ParseCIDR(a.String())
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp", ip.String()+":18080", 300*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Errorf("REST server reachable on %s", ip)
		}
	}
}
