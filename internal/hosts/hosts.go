// Package hosts 管理登记过的其他主机(PRD 5.4、G5):名字、地址和访问令牌,
// 存在数据目录的 hosts.json 里,只有当前用户可读。命令行的 --host 和 bugu host、
// 应用里的"另一个 Bugu"共用这一份(决策记录 0015)。
package hosts

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"bugu/internal/i18n"
)

// File 是登记表在数据目录里的文件名。
const File = "hosts.json"

// Host 是一台登记过的主机。
type Host struct {
	Name  string `json:"name"`
	URL   string `json:"url"`   // 例如 https://bugu.example.com 或 http://100.64.0.8:7377
	Token string `json:"token"` // 那台主机发给这里的令牌,发请求时要带上,所以只能存明文
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Validate 检查名字和地址,并把地址整理成不带结尾斜杠的形式。
func Validate(h Host) (Host, error) {
	if !namePattern.MatchString(h.Name) {
		return h, i18n.Errorf("主机名 %q 不合法:只能包含字母、数字、-、_ 和 .,以字母或数字开头,最长 64 个字符", h.Name)
	}
	u, err := url.Parse(strings.TrimSpace(h.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return h, i18n.Errorf("地址 %q 不合法,应写成 http://主机:端口 或 https://主机 这样,不带路径", h.URL)
	}
	h.URL = u.Scheme + "://" + u.Host
	if strings.TrimSpace(h.Token) == "" {
		return h, i18n.Errorf("缺少那台主机的令牌")
	}
	h.Token = strings.TrimSpace(h.Token)
	return h, nil
}

// PlainPublic 报告地址是否是经公网的明文 HTTP(C6):令牌和任务内容会以明文经过不受信任的网络。
// 本机、局域网、组网工具(如 Tailscale 的 100.64.0.0/10 和 .ts.net)和 HTTPS 都不算。
// 不做域名解析,认不出的域名按公网算,宁可多提醒一次。
func PlainPublic(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"localhost", ".local", ".lan", ".internal", ".home.arpa", ".ts.net"} {
		if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
			return false
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

// Load 读取登记表,文件不存在时返回空表。
func Load(home string) ([]Host, error) {
	raw, err := os.ReadFile(filepath.Join(home, File))
	if errors.Is(err, fs.ErrNotExist) {
		return []Host{}, nil
	} else if err != nil {
		return nil, err
	}
	var list []Host
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, i18n.Errorf("%s 不是合法的 JSON: %v", filepath.Join(home, File), err)
	}
	return list, nil
}

// Find 按名字找一台登记过的主机。
func Find(home, name string) (Host, error) {
	list, err := Load(home)
	if err != nil {
		return Host{}, err
	}
	if i := slices.IndexFunc(list, func(h Host) bool { return h.Name == name }); i >= 0 {
		return list[i], nil
	}
	return Host{}, ErrUnknown{name}
}

// ErrUnknown 表示没有登记这个名字的主机。
type ErrUnknown struct{ Name string }

func (e ErrUnknown) Error() string {
	return i18n.T("没有登记名为 %q 的主机,可用 bugu host list 查看,用 bugu host add 登记", e.Name)
}

// Put 登记一台主机,同名的替换。
func Put(home string, h Host) error {
	list, err := Load(home)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(x Host) bool { return x.Name == h.Name })
	list = append(list, h)
	slices.SortFunc(list, func(a, b Host) int { return strings.Compare(a.Name, b.Name) })
	return save(home, list)
}

// Remove 删除一台登记过的主机。
func Remove(home, name string) error {
	list, err := Load(home)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(x Host) bool { return x.Name == name })
	if len(kept) == len(list) {
		return ErrUnknown{name}
	}
	return save(home, kept)
}

// save 先写临时文件再改名,读的一方不会读到写了一半的文件。文件里有令牌,只给当前用户。
func save(home string, list []Host) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, File+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(home, File))
}
