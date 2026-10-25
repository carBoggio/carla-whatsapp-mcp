package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// make sure the mtime changes even on coarse filesystems
	future := time.Now().Add(time.Duration(len(content)) * time.Millisecond)
	os.Chtimes(path, future, future)
}

func TestNormalizeTarget(t *testing.T) {
	ok := map[string]string{
		"34600111222":                   "34600111222",
		"+34 600 111 222":               "34600111222",
		"(34) 600-111-222":              "34600111222",
		"34600111222@s.whatsapp.net":    "34600111222",
		"34600111222:12@s.whatsapp.net": "34600111222",
		"120363012345678901@g.us":       "120363012345678901@g.us",
	}
	for in, want := range ok {
		got, err := NormalizeTarget(in)
		if err != nil || got != want {
			t.Errorf("NormalizeTarget(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", "123", "abc", "34600111222; rm -rf", "status@broadcast",
		"123456789@lid", "1234567890123456", "123@newsletter"}
	for _, in := range bad {
		if got, err := NormalizeTarget(in); err == nil {
			t.Errorf("NormalizeTarget(%q) = %q, want error", in, got)
		}
	}
}

func TestFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")

	a := NewAllowlist(path) // missing file
	if a.CanSend("34600111222") || a.CanRead("34600111222") {
		t.Fatal("missing file must deny everything")
	}
	writeFile(t, path, "{not json")
	if a.CanSend("34600111222") || a.CanRead("34600111222") {
		t.Fatal("invalid JSON must deny everything")
	}
	writeFile(t, path, `{"send":[],"read":[]}`)
	if a.CanSend("34600111222") {
		t.Fatal("empty list must deny")
	}
}

func TestReloadAndSeparation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")
	writeFile(t, path, `{"send":["+34 600 111 222"],"read":["120363012345678901@g.us"]}`)
	a := NewAllowlist(path)

	if !a.CanSend("34600111222@s.whatsapp.net") {
		t.Error("should send to listed number given as JID")
	}
	if a.CanRead("34600111222") {
		t.Error("send permission must not imply read")
	}
	if !a.CanRead("120363012345678901@g.us") || a.CanSend("120363012345678901@g.us") {
		t.Error("group read/send separation broken")
	}
	if a.CanSend("34600999999") {
		t.Error("unlisted number must be denied")
	}

	// external edit (like the wa-allow CLI) is picked up without restart
	writeFile(t, path, `{"send":["34600999999"],"read":[]}`)
	if !a.CanSend("34600999999") || a.CanSend("34600111222") {
		t.Error("reload on change failed")
	}
	// deleting the file denies everything again
	os.Remove(path)
	if a.CanSend("34600999999") {
		t.Error("removed file must deny everything")
	}
}

func TestUpdatePersistsAndCallsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")
	writeFile(t, path, `{"send":[],"read":[]}`)
	a := NewAllowlist(path)
	changed := make(chan struct{}, 10)
	a.onChange = func() { changed <- struct{}{} }

	if _, err := a.Update("send", "+34 600 111 222", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Update("read", "34600111222", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("onChange not called")
	}
	b := NewAllowlist(path) // fresh reader sees the file
	if !b.CanSend("34600111222") || !b.CanRead("34600111222") {
		t.Fatal("update not persisted")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", st.Mode().Perm())
	}
	if _, err := a.Update("send", "34600111222", false); err != nil {
		t.Fatal(err)
	}
	if a.CanSend("34600111222") || !a.CanRead("34600111222") {
		t.Error("removing send must keep read")
	}
	if _, err := a.Update("send", "not-a-number", true); err == nil {
		t.Error("invalid input must be rejected")
	}
	if _, err := a.Update("admin", "34600111222", true); err == nil {
		t.Error("unknown list must be rejected")
	}
}

func own() *types.JID {
	j := types.JID{User: "34611000000", Server: types.DefaultUserServer, Device: 0}
	return &j
}

func info(fromMe bool, senderDevice uint16, sender, chat string) types.MessageInfo {
	return types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     types.JID{User: chat, Server: types.DefaultUserServer},
			Sender:   types.JID{User: sender, Server: types.DefaultUserServer, Device: senderDevice},
			IsFromMe: fromMe,
		},
	}
}

func TestIsOwnerCommand(t *testing.T) {
	me := "34611000000"
	cases := []struct {
		name string
		info types.MessageInfo
		text string
		want bool
	}{
		{"phone in self chat", info(true, 0, me, me), "/permitir 34600111222", true},
		{"linked device (bridge/web/desktop)", info(true, 7, me, me), "/permitir 34600111222", false},
		{"not from me", info(false, 0, "34699999999", me), "/permitir 34600111222", false},
		{"other chat", info(true, 0, me, "34600111222"), "/permitir 34600111222", false},
		{"not a command", info(true, 0, me, me), "hola", false},
	}
	for _, c := range cases {
		if got := isOwnerCommand(own(), c.info, c.text); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if isOwnerCommand(nil, info(true, 0, me, me), "/lista") {
		t.Error("nil own JID must be rejected")
	}
	lidSender := info(true, 0, me, me)
	lidSender.Sender.Server = types.HiddenUserServer
	if isOwnerCommand(own(), lidSender, "/lista") {
		t.Error("LID sender must be rejected (fail closed)")
	}
}

func TestRunCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.json")
	writeFile(t, path, `{"send":[],"read":[]}`)
	purged := ""
	env := commandEnv{
		allow:     NewAllowlist(path),
		audit:     &AuditLog{path: filepath.Join(dir, "audit.log")},
		purgeChat: func(k string) error { purged = k; return nil },
	}

	if r := runCommand(env, "/permitir +34 600 111 222"); !strings.Contains(r, "34600111222") {
		t.Errorf("permitir reply: %s", r)
	}
	if !env.allow.CanSend("34600111222") || env.allow.CanRead("34600111222") {
		t.Error("/permitir must only grant send")
	}
	runCommand(env, "/permitir-leer 34600111222")
	if !env.allow.CanRead("34600111222") {
		t.Error("/permitir-leer failed")
	}
	if r := runCommand(env, "/lista"); !strings.Contains(r, "34600111222") {
		t.Errorf("lista: %s", r)
	}
	runCommand(env, "/quitar-envio 34600111222")
	if env.allow.CanSend("34600111222") || !env.allow.CanRead("34600111222") || purged != "" {
		t.Error("/quitar-envio must keep read and not purge")
	}
	runCommand(env, "/quitar 34600111222")
	if env.allow.CanRead("34600111222") || purged != "34600111222" {
		t.Error("/quitar must remove read and purge")
	}
	if r := runCommand(env, "/permitir"); !strings.Contains(r, "Falta") {
		t.Errorf("missing arg: %s", r)
	}
	if r := runCommand(env, "/permitir hola"); !strings.Contains(r, "No se pudo") {
		t.Errorf("invalid arg: %s", r)
	}
	if r := runCommand(env, "/borrar-todo"); !strings.Contains(r, "desconocido") {
		t.Errorf("unknown: %s", r)
	}
	logData, _ := os.ReadFile(filepath.Join(dir, "audit.log"))
	if !strings.Contains(string(logData), "PHONE add send 34600111222") {
		t.Error("audit log missing entry")
	}
}
