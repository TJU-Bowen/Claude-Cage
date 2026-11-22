package workstationlan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"golang.org/x/crypto/ssh"
)

type Guest interface {
	ChangeDevice(context.Context, ssh.PublicKey, bool) (GuestInfo, error)
	Dial(context.Context) (net.Conn, error)
}
type LimaGuest struct{ Profile, SSH string }

func (g LimaGuest) config() (string, string) {
	name := cage.InstanceName(g.Profile, cage.EnvironmentDir(g.Profile))
	return "lima-" + name, filepath.Join(g.Profile, "state", "lima", name, "ssh.config")
}
func (g LimaGuest) Dial(ctx context.Context) (net.Conn, error) {
	_, path := g.config()
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("Cage 客机尚未就绪")
	}
	var ip net.IP
	var port int
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "hostname":
			ip = net.ParseIP(fields[1])
		case "port":
			port, _ = strconv.Atoi(fields[1])
		}
	}
	if ip == nil || !ip.IsLoopback() || port < 1 || port > 65535 {
		return nil, errors.New("当前实例没有有效的本机 SSH 入口")
	}
	d := net.Dialer{Timeout: 4 * time.Second}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
}

// Arguments arrive on stdin. Only the pubkey line owned by this pairing service is replaced.
const guestKeys = `set -eu
umask 077
IFS= read -r action
IFS= read -r pub
IFS= read -r tag
case "$action" in add|remove) ;; *) exit 2 ;; esac
cd "$HOME"
mkdir -p .ssh
cd .ssh
test ! -L authorized_keys
exec 9>cage-lan.lock
flock -x -w 8 9
file=authorized_keys
tmp=$(mktemp .cage-lan-XXXXXXXX)
trap 'rm -f "$tmp"' EXIT
if test -f "$file"; then
    awk -v tag="$tag" -v pub="$pub" '!(NF == 4 && $1 == "restrict" && $2 == "ssh-ed25519" && $3 == pub && $4 == tag)' "$file" > "$tmp"
fi
if test "$action" = add; then
    printf 'restrict ssh-ed25519 %s %s\n' "$pub" "$tag" >> "$tmp"
fi
if test -f "$file" && cmp -s "$tmp" "$file"; then :; else mv -f "$tmp" "$file"; fi
id -un
cat /etc/ssh/ssh_host_ed25519_key.pub`

var accountName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}[$]?$`)

func (g LimaGuest) ChangeDevice(ctx context.Context, key ssh.PublicKey, add bool) (GuestInfo, error) {
	if key.Type() != ssh.KeyAlgoED25519 {
		return GuestInfo{}, errors.New("不支持此设备身份")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	alias, path := g.config()
	script := "sh -c '" + strings.ReplaceAll(guestKeys, "'", "'\\''") + "'"
	cmd := exec.CommandContext(ctx, g.SSH, "-F", path, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-T", alias, script)
	hideChild(cmd)
	action := "remove"
	if add {
		action = "add"
	}
	cmd.Stdin = strings.NewReader(action + "\n" + wireKey(key) + "\ncage-workstation-" + idFor(key) + "\n")
	var b bytes.Buffer
	cmd.Stdout = &b
	if e := cmd.Run(); e != nil {
		return GuestInfo{}, fmt.Errorf("客机设备授权未完成，请确认 Cage 环境运行正常后重试")
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !accountName.MatchString(strings.TrimSpace(lines[0])) {
		return GuestInfo{}, errors.New("客机返回的账户信息无效")
	}
	k, _, _, _, e := ssh.ParseAuthorizedKey([]byte(lines[1]))
	if e != nil || k.Type() != ssh.KeyAlgoED25519 {
		return GuestInfo{}, errors.New("客机 SSH 身份无效")
	}
	return GuestInfo{strings.TrimSpace(lines[0]), wireKey(k)}, nil
}
