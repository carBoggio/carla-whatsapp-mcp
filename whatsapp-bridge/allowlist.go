package main

// Allowlist: which chats the bridge may STORE/READ and which recipients it may SEND to.
//
// Security model
//   - Fail closed: if the file is missing, unreadable or invalid, nothing is allowed.
//   - The file is re-read automatically when its modification time changes,
//     so edits (from the phone commands or the wa-allow CLI) apply without a restart.
//   - Phone numbers are stored as plain digits ("34600111222"); groups as their
//     full JID ("1203630xxxxxxxxx@g.us"). Anything else (LIDs, broadcasts,
//     newsletters, status) is never allowed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

type allowlistFile struct {
	Send []string `json:"send"`
	Read []string `json:"read"`
}

type Allowlist struct {
	path     string
	mu       sync.Mutex
	modTime  time.Time
	size     int64
	send     map[string]bool
	read     map[string]bool
	onChange func()
	logf     func(format string, args ...interface{})
}

func NewAllowlist(path string) *Allowlist {
	a := &Allowlist{
		path: path,
		send: map[string]bool{},
		read: map[string]bool{},
		logf: func(format string, args ...interface{}) { fmt.Printf("[allowlist] "+format+"\n", args...) },
	}
	a.mu.Lock()
	a.refreshLocked()
	a.mu.Unlock()
	return a
}

// NormalizeTarget turns a phone number or JID into the canonical key used in the lists.
func NormalizeTarget(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("vacío")
	}
	if strings.Contains(s, "@") {
		jid, err := types.ParseJID(s)
		if err != nil {
			return "", fmt.Errorf("JID inválido: %v", err)
		}
		jid = jid.ToNonAD()
		switch jid.Server {
		case types.DefaultUserServer:
			return validateDigits(jid.User)
		case types.GroupServer:
			if jid.User == "" {
				return "", errors.New("grupo inválido")
			}
			return jid.User + "@" + types.GroupServer, nil
		default:
			return "", fmt.Errorf("tipo de chat no soportado: %s", jid.Server)
		}
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			// separators allowed in human input
		default:
			return "", fmt.Errorf("carácter no válido en número: %q", r)
		}
	}
	return validateDigits(b.String())
}

func validateDigits(d string) (string, error) {
	if len(d) < 7 || len(d) > 15 {
		return "", fmt.Errorf("número con longitud inválida (%d dígitos); usa formato internacional con código de país", len(d))
	}
	for _, r := range d {
		if r < '0' || r > '9' {
			return "", errors.New("el número solo puede tener dígitos")
		}
	}
	return d, nil
}

// refreshLocked reloads the file if it changed. Caller holds a.mu.
func (a *Allowlist) refreshLocked() {
	st, err := os.Stat(a.path)
	if err != nil {
		if len(a.send) > 0 || len(a.read) > 0 || a.modTime.IsZero() {
			a.logf("no se puede leer %s (%v): se deniega todo", a.path, err)
		}
		a.setLocked(nil, nil, time.Unix(1, 0), -1)
		return
	}
	if st.ModTime().Equal(a.modTime) && st.Size() == a.size {
		return
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		a.logf("error leyendo %s (%v): se deniega todo", a.path, err)
		a.setLocked(nil, nil, st.ModTime(), st.Size())
		return
	}
	var f allowlistFile
	if err := json.Unmarshal(data, &f); err != nil {
		a.logf("JSON inválido en %s (%v): se deniega todo", a.path, err)
		a.setLocked(nil, nil, st.ModTime(), st.Size())
		return
	}
	a.setLocked(f.Send, f.Read, st.ModTime(), st.Size())
	a.logf("lista cargada: %d para enviar, %d para leer", len(a.send), len(a.read))
}

func (a *Allowlist) setLocked(send, read []string, mod time.Time, size int64) {
	ns, nr := map[string]bool{}, map[string]bool{}
	for _, s := range send {
		if k, err := NormalizeTarget(s); err == nil {
			ns[k] = true
		} else {
			a.logf("entrada ignorada en send %q: %v", s, err)
		}
	}
	for _, s := range read {
		if k, err := NormalizeTarget(s); err == nil {
			nr[k] = true
		} else {
			a.logf("entrada ignorada en read %q: %v", s, err)
		}
	}
	changed := !sameSet(ns, a.send) || !sameSet(nr, a.read)
	a.send, a.read, a.modTime, a.size = ns, nr, mod, size
	if changed && a.onChange != nil {
		go a.onChange()
	}
}

func sameSet(x, y map[string]bool) bool {
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

func (a *Allowlist) check(target string, read bool) bool {
	k, err := NormalizeTarget(target)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshLocked()
	if read {
		return a.read[k]
	}
	return a.send[k]
}

func (a *Allowlist) CanSend(target string) bool { return a.check(target, false) }
func (a *Allowlist) CanRead(target string) bool { return a.check(target, true) }

// Snapshot returns sorted copies of both lists.
func (a *Allowlist) Snapshot() (send, read []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshLocked()
	for k := range a.send {
		send = append(send, k)
	}
	for k := range a.read {
		read = append(read, k)
	}
	sort.Strings(send)
	sort.Strings(read)
	return
}

// Update applies a change and writes the file atomically (temp file + rename, mode 0600).
// kind: "send" or "read". add=false removes.
func (a *Allowlist) Update(kind, target string, add bool) (string, error) {
	k, err := NormalizeTarget(target)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refreshLocked()
	ns, nr := copySet(a.send), copySet(a.read)
	switch kind {
	case "send":
		setOrDelete(ns, k, add)
	case "read":
		setOrDelete(nr, k, add)
	default:
		return "", fmt.Errorf("lista desconocida %q", kind)
	}
	if err := a.writeLocked(ns, nr); err != nil {
		return "", err
	}
	return k, nil
}

func copySet(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m))
	for k := range m {
		c[k] = true
	}
	return c
}

func setOrDelete(m map[string]bool, k string, add bool) {
	if add {
		m[k] = true
	} else {
		delete(m, k)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *Allowlist) writeLocked(send, read map[string]bool) error {
	data, err := json.MarshalIndent(allowlistFile{Send: keys(send), Read: keys(read)}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(a.path)
	tmp, err := os.CreateTemp(dir, ".allowlist-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, a.path); err != nil {
		return err
	}
	st, err := os.Stat(a.path)
	if err != nil {
		return err
	}
	a.setLocked(keys(send), keys(read), st.ModTime(), st.Size())
	return nil
}

// ---- audit log ----

type AuditLog struct {
	path string
	mu   sync.Mutex
}

func (l *AuditLog) Printf(format string, args ...interface{}) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	fmt.Print("[audit] " + line)
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Printf("[audit] no se pudo escribir el log: %v\n", err)
		return
	}
	defer f.Close()
	f.WriteString(line)
}
