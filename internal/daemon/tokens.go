package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"slices"

	"bugu/internal/api"
	"bugu/internal/i18n"
	"bugu/internal/server"
	"bugu/internal/store"
)

// 访问令牌(C5,决策记录 0015):数据目录里 token 文件的本机令牌,加上另外创建、可以单独作废的令牌。
// 另外创建的只存哈希,明文只在创建时返回一次。

var tokenName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// LocalTokenName 是本机令牌在列表里的名字。
const LocalTokenName = "local"

// access 判断一个令牌能访问到什么程度:本机令牌是 Local,另外创建的是 Shared。
func (e *engine) access(got string) server.Access {
	if subtle.ConstantTimeCompare([]byte(got), []byte(e.localToken)) == 1 {
		return server.Local
	}
	sum := sha256.Sum256([]byte(got))
	want := hex.EncodeToString(sum[:])
	e.tmu.RLock()
	defer e.tmu.RUnlock()
	ok := false
	for _, t := range e.tokens {
		// 逐个比较完,不因为先找到而提前返回。
		if subtle.ConstantTimeCompare([]byte(t.SHA256), []byte(want)) == 1 {
			ok = true
		}
	}
	if ok {
		return server.Shared
	}
	return server.Denied
}

func (e *engine) loadTokens() error {
	list, err := e.store.LoadTokens()
	if err != nil {
		return err
	}
	e.tmu.Lock()
	e.tokens = list
	e.tmu.Unlock()
	return nil
}

// Tokens 列出全部令牌,本机令牌在前。
func (e *engine) Tokens() ([]api.Token, error) {
	e.tmu.RLock()
	defer e.tmu.RUnlock()
	out := []api.Token{{Name: LocalTokenName, Local: true}}
	for _, t := range e.tokens {
		out = append(out, api.Token{Name: t.Name, CreatedAt: t.CreatedAt})
	}
	return out, nil
}

// CreateToken 创建一个令牌,返回它的明文。名字不能重复。
func (e *engine) CreateToken(name string) (api.NewToken, error) {
	if !tokenName.MatchString(name) || name == LocalTokenName {
		return api.NewToken{}, invalid(i18n.Errorf("令牌名 %q 不合法:只能包含字母、数字、-、_ 和 .,以字母或数字开头,最长 64 个字符,不能叫 %s", name, LocalTokenName))
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return api.NewToken{}, err
	}
	plain := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(plain))
	e.tmu.Lock()
	defer e.tmu.Unlock()
	if slices.ContainsFunc(e.tokens, func(t store.StoredToken) bool { return t.Name == name }) {
		return api.NewToken{}, api.Wrap(api.CodeConflict, i18n.Errorf("已有名为 %q 的令牌;要换一个,先用 bugu token revoke 作废它", name))
	}
	next := append(slices.Clone(e.tokens), store.StoredToken{Name: name, SHA256: hex.EncodeToString(sum[:]), CreatedAt: e.clock.Now()})
	if err := e.store.SaveTokens(next); err != nil {
		return api.NewToken{}, err
	}
	e.tokens = next
	return api.NewToken{Name: name, Token: plain}, nil
}

// RevokeToken 作废一个令牌,立即生效。本机令牌不能作废。
func (e *engine) RevokeToken(name string) error {
	if name == LocalTokenName {
		return invalid(i18n.Errorf("本机令牌不能作废:本机的命令行和应用靠它连接。要更换它,停止后台后删除数据目录里的 token 文件再启动"))
	}
	e.tmu.Lock()
	defer e.tmu.Unlock()
	next := slices.DeleteFunc(slices.Clone(e.tokens), func(t store.StoredToken) bool { return t.Name == name })
	if len(next) == len(e.tokens) {
		return api.Wrap(api.CodeNotFound, i18n.Errorf("没有名为 %q 的令牌,可用 bugu token list 查看", name))
	}
	if err := e.store.SaveTokens(next); err != nil {
		return err
	}
	e.tokens = next
	return nil
}
