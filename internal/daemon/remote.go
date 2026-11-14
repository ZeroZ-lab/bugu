package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"bugu/internal/api"
	"bugu/internal/exec"
	"bugu/internal/hosts"
	"bugu/internal/i18n"
	"bugu/internal/runner"
)

// probeTimeout 是检查另一台主机时每次请求的时长上限。
const probeTimeout = 5 * time.Second

// withTimeout 返回一个到时间就取消的 context,计时用注入的时钟。
func (e *engine) withTimeout(d time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(e.ctx)
	timer := e.clock.NewTimer(d)
	go func() {
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { timer.Stop(); cancel() }
}

// Hosts 列出登记的其他主机。probe 为真时逐个检查连接状态(G14):连不上记为离线,
// 不把那台主机的任务算作失败。各台同时检查,整体最多等 probeTimeout。
func (e *engine) Hosts(probe bool) ([]api.Host, error) {
	list, err := hosts.Load(e.store.Dir())
	if err != nil {
		return nil, err
	}
	out := make([]api.Host, len(list))
	var wg sync.WaitGroup
	for i, h := range list {
		out[i] = api.Host{Name: h.Name, URL: h.URL, Token: h.Token, PlainPublic: hosts.PlainPublic(h.URL)}
		if !probe {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i].Status, out[i].Version = e.probe(h)
		}()
	}
	wg.Wait()
	return out, nil
}

// probe 检查一台主机能否连上、令牌是否被接受。
func (e *engine) probe(h hosts.Host) (status, version string) {
	ctx, done := e.withTimeout(probeTimeout)
	defer done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL+api.Prefix+"/tasks", nil)
	if err != nil {
		return api.HostOffline, ""
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := e.http.Do(req)
	if err != nil {
		return api.HostOffline, ""
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return api.HostUnauthorized, ""
	case resp.StatusCode != http.StatusOK:
		return api.HostOffline, ""
	}
	// 令牌有效,再取一次版本号;取不到不影响状态。
	if req, err = http.NewRequestWithContext(ctx, http.MethodGet, h.URL+api.Prefix+"/health", nil); err == nil {
		req.Header.Set("Authorization", "Bearer "+h.Token)
		if resp, err := e.http.Do(req); err == nil {
			var health api.Health
			_ = json.NewDecoder(resp.Body).Decode(&health)
			resp.Body.Close()
			version = health.Version
		}
	}
	return api.HostOnline, version
}

// PutHost 登记一台主机,同名的替换。返回的结果里注明是否是经公网的明文 HTTP(C6)。
func (e *engine) PutHost(in api.Host) (api.Host, error) {
	h, err := hosts.Validate(hosts.Host{Name: in.Name, URL: in.URL, Token: in.Token})
	if err != nil {
		return api.Host{}, invalid(err)
	}
	if err := hosts.Put(e.store.Dir(), h); err != nil {
		return api.Host{}, err
	}
	status, version := e.probe(h)
	return api.Host{Name: h.Name, URL: h.URL, Status: status, Version: version, PlainPublic: hosts.PlainPublic(h.URL)}, nil
}

// RemoveHost 删除一台登记过的主机。
func (e *engine) RemoveHost(name string) error {
	err := hosts.Remove(e.store.Dir(), name)
	if errors.As(err, new(hosts.ErrUnknown)) {
		return api.Wrap(api.CodeNotFound, err)
	}
	return err
}

// SaveRunner 检查并保存一份用户自己的运行器配置,同名的替换;与预设同名时,用户的优先(R1、R3)。
func (e *engine) SaveRunner(name string, raw []byte) (api.Runner, error) {
	var rn runner.Runner
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rn); err != nil {
		return api.Runner{}, invalid(i18n.Errorf("运行器配置不是合法的 JSON: %v", err))
	}
	if rn.Name == "" {
		rn.Name = name
	}
	if rn.Name != name {
		return api.Runner{}, invalid(i18n.Errorf("配置里的 name 是 %q,与要保存的 %q 不一致", rn.Name, name))
	}
	rn.Preset = false
	if err := runner.Validate(rn); err != nil {
		return api.Runner{}, invalid(err)
	}
	b, err := json.MarshalIndent(rn, "", "  ")
	if err != nil {
		return api.Runner{}, err
	}
	if err := e.store.SaveRunnerConfig(name, b); err != nil {
		return api.Runner{}, err
	}
	list, err := e.Runners()
	if err != nil {
		return api.Runner{}, err
	}
	for _, r := range list {
		if r.Name == name {
			return r, nil
		}
	}
	return api.Runner{Name: name}, nil
}

// Plan 说明立即运行一次会执行什么,不启动任何进程(bugu run --dry-run)。
func (e *engine) Plan(name string) (api.Plan, error) {
	t, err := e.load(name)
	if err != nil {
		return api.Plan{}, err
	}
	info := exec.Info{LastRun: e.lastSuccess(t.Name)}
	if info.StateDir, err = e.store.StateDir(t.Name); err != nil {
		return api.Plan{}, err
	}
	plan, err := e.exec.Plan(t, info)
	if err != nil {
		return api.Plan{}, invalid(err)
	}
	return plan, nil
}
