// Package workstationlan pairs workstations with the user's existing Cage guest.
package workstationlan

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/sylvinus/agent-vm/internal/host"
	"golang.org/x/crypto/ssh"
)

const ServicePort = 17942
const serviceName = "_claude-cage._tcp.local."

// Public is the SSH wire encoding, without comments or private key material.
type Peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Public  string `json:"public"`
	Address string `json:"address"`
}
type GuestInfo struct {
	User   string `json:"user"`
	Public string `json:"public"`
}
type Device struct {
	Peer
	Disabled bool `json:"disabled,omitempty"`
}
type SavedServer struct {
	Peer
	Guest       GuestInfo `json:"guest"`
	DesktopPort int       `json:"desktopPort"`
}
type ClientRecord struct {
	Servers map[string]SavedServer `json:"servers"`
	Last    string                 `json:"last,omitempty"`
}

func idFor(k ssh.PublicKey) string {
	sum := sha256.Sum256(k.Marshal())
	return hex.EncodeToString(sum[:])
}
func wireKey(k ssh.PublicKey) string { return base64.StdEncoding.EncodeToString(k.Marshal()) }
func decodeKey(v string) (ssh.PublicKey, error) {
	b, e := base64.StdEncoding.DecodeString(v)
	if e != nil || len(b) > 150 {
		return nil, errors.New("无效的设备身份")
	}
	k, e := ssh.ParsePublicKey(b)
	if e != nil || k.Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("设备身份需要 Ed25519 公钥")
	}
	return k, nil
}
func cleanName(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
	r := []rune(s)
	if len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		s = "Cage 工作机"
	}
	return s
}
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = io.ReadFull(rand.Reader, b)
	return hex.EncodeToString(b)
}
func writeFile(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".lan-*.tmp")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = host.ProtectFile(name); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return writeFile(path, b)
}
func readJSON(path string, v any) error {
	if e := host.CheckPrivateFile(path); e != nil {
		return e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func initIdentity(dir string) (ssh.Signer, error) {
	if e := host.EnsurePrivateDir(dir); e != nil {
		return nil, e
	}
	p := filepath.Join(dir, "identity")
	if e := host.CheckPrivateFile(p); errors.Is(e, os.ErrNotExist) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		block, e := ssh.MarshalPrivateKey(key, "Cage workstation LAN")
		if e != nil {
			return nil, e
		}
		// Link an already private, complete file into place. A second launch can only read the winner.
		tmp := filepath.Join(dir, ".identity-"+randomID(12))
		defer os.Remove(tmp)
		if e = writeFile(tmp, pem.EncodeToMemory(block)); e != nil {
			return nil, e
		}
		if e = os.Link(tmp, p); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if e := host.CheckPrivateFile(p); e != nil {
		return nil, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, e
	}
	signer, e := ssh.ParsePrivateKey(b)
	if e != nil {
		return nil, e
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("无法识别工作机身份")
	}
	return signer, nil
}
