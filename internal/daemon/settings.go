package daemon

import (
	"os"

	"bugu/internal/api"
	"bugu/internal/i18n"
)

// Health 报告后台在运行,以及程序的版本。
func (e *engine) Health() api.Health {
	host, _ := os.Hostname()
	return api.Health{OK: true, Version: e.version, Host: host}
}

// Settings 返回后台的设置,以及实际说的语言(决策记录 0014)。
func (e *engine) Settings() (api.Settings, error) {
	s, err := e.loadSettings()
	s.Effective = string(i18n.Current())
	return s, err
}

// UpdateSettings 检查并保存设置,随即按新的语言说话:之后的通知、错误信息和菜单栏都用它。
func (e *engine) UpdateSettings(s api.Settings) (api.Settings, error) {
	switch s.Language {
	case api.LanguageAuto, api.LanguageZh, api.LanguageEn:
	case "":
		s.Language = api.LanguageAuto
	default:
		return api.Settings{}, invalid(i18n.Errorf("语言 %q 不合法,可用的是 auto(跟随系统)、zh、en", s.Language))
	}
	s.Effective = ""
	e.smu.Lock()
	defer e.smu.Unlock()
	if err := e.store.SaveSettings(s); err != nil {
		return api.Settings{}, err
	}
	i18n.Set(effective(s.Language))
	s.Effective = string(i18n.Current())
	return s, nil
}

func (e *engine) loadSettings() (api.Settings, error) {
	e.smu.Lock()
	defer e.smu.Unlock()
	s := api.Settings{Language: api.LanguageAuto}
	err := e.store.LoadSettings(&s)
	return s, err
}

// applySettings 在启动时按保存的设置决定后台的语言。
func (e *engine) applySettings() {
	s, err := e.loadSettings()
	i18n.Set(effective(s.Language))
	if err != nil {
		e.log(i18n.T("读取设置失败,按默认设置运行: %v", err))
	}
}

// effective 返回一个语言设置实际对应的语言。
func effective(setting string) i18n.Lang {
	switch l := i18n.Lang(setting); l {
	case i18n.Zh, i18n.En:
		return l
	}
	return i18n.System()
}
