package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugu/internal/i18n"
	"bugu/internal/task"
)

// ErrNotFound 表示请求的任务或运行不存在。
var ErrNotFound = errors.New("store: not found")

// 数据目录的布局:
//
//	tasks/<任务名>.json       任务定义,一个任务一个文件
//	runs/<运行编号>/run.json   运行的元数据
//	runs/<运行编号>/task.json  运行开始时的任务配置快照
//	runs/<运行编号>/stdout.log、stderr.log  本次运行的结果:最后执行的那一步的输出,各步的标准错误
//	runs/<运行编号>/steps/<序号>/          每个步骤的 stdout.log、stderr.log,以及 AI 命令行步骤
//	                                      实际发出的 prompt.txt,由执行器写入
//	runs/<运行编号>/result.json            任务自己写的结果信封(可选,决策记录 0012)
//	runs/<运行编号>/deliveries/<序号>.json  每条通知的投递记录,序号是任务的第几条通知设置(N7)
//	state/<任务名>/            任务的状态目录,跨次运行保留,供任务自己存放数据
//	runners/<运行器名>.json    用户自己的运行器配置,由 runner 包读取
//	state.json                每个任务已评估到的时间点
//	settings.json             后台的设置(界面语言等)
//	tokens.json               另外创建的访问令牌:名字、SHA-256 哈希和创建时间,不存明文(C5)
//	hosts.json                登记的其他主机,由 hosts 包读写
type Store struct {
	dir string

	mu        sync.Mutex
	idLoaded  bool
	lastStamp string // 最近一个运行编号的时间部分
	lastSeq   int    // 以及序号部分

	runs runIndex // 运行记录的内存索引,见 runindex.go
}

// Open 打开数据目录,不存在时创建。目录只对当前用户开放,因为其中有令牌和任务输出。
func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "tasks"), filepath.Join(dir, "runs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir}, nil
}

func (s *Store) taskPath(name string) (string, error) {
	if !task.ValidName(name) {
		return "", fmt.Errorf("store: invalid task name %q", name)
	}
	return filepath.Join(s.dir, "tasks", name+".json"), nil
}

// SaveTask 写入任务定义,已存在则覆盖。
func (s *Store) SaveTask(t task.Task) error {
	path, err := s.taskPath(t.Name)
	if err != nil {
		return err
	}
	return writeJSON(path, t)
}

func (s *Store) LoadTask(name string) (task.Task, error) {
	var t task.Task
	path, err := s.taskPath(name)
	if err != nil {
		return t, ErrNotFound
	}
	return t, readJSON(path, &t)
}

// ListTasks 返回全部任务,按名字排序。
func (s *Store) ListTasks() ([]task.Task, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "tasks"))
	if err != nil {
		return nil, err
	}
	var tasks []task.Task
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !task.ValidName(name) {
			continue
		}
		t, err := s.LoadTask(name)
		if err != nil {
			return nil, i18n.Errorf("读取任务文件 %s: %w", e.Name(), err)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

func (s *Store) DeleteTask(name string) error {
	path, err := s.taskPath(name)
	if err != nil {
		return ErrNotFound
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

var runIDPattern = regexp.MustCompile(`^([0-9]{8}T[0-9]{6})-([0-9]{6})$`)

// NewRunID 生成运行编号:UTC 时间加同一秒内的序号。编号严格递增,按字典序排列即按创建顺序排列;
// 同一秒内的多次运行、重启、时钟回拨都不会打乱顺序。
func (s *Store) NewRunID(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.idLoaded {
		entries, err := os.ReadDir(filepath.Join(s.dir, "runs"))
		if err != nil {
			return "", err
		}
		for _, e := range slices.Backward(entries) {
			if m := runIDPattern.FindStringSubmatch(e.Name()); m != nil {
				s.lastStamp = m[1]
				s.lastSeq, _ = strconv.Atoi(m[2])
				break
			}
		}
		s.idLoaded = true
	}
	if stamp := now.UTC().Format("20060102T150405"); stamp > s.lastStamp {
		s.lastStamp, s.lastSeq = stamp, 0
	} else {
		s.lastSeq++
	}
	return fmt.Sprintf("%s-%06d", s.lastStamp, s.lastSeq), nil
}

func (s *Store) runDir(id string) (string, error) {
	if !runIDPattern.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.dir, "runs", id), nil
}

// RunDir 返回一次运行的存档目录。
func (s *Store) RunDir(id string) (string, error) { return s.runDir(id) }

// StateDir 返回任务的状态目录,不存在时创建。删除任务时不删除它,
// 以免误删用户的数据;同名的任务重新建出来会接着使用。
func (s *Store) StateDir(name string) (string, error) {
	if !task.ValidName(name) {
		return "", fmt.Errorf("store: invalid task name %q", name)
	}
	dir := filepath.Join(s.dir, "state", name)
	return dir, os.MkdirAll(dir, 0o700)
}

// RunnersDir 返回用户自己的运行器配置所在的目录,它可能尚不存在。
func (s *Store) RunnersDir() string { return filepath.Join(s.dir, "runners") }

// CreateRun 建立一次运行的存档,并保存当时的任务配置快照(O11)。
func (s *Store) CreateRun(r task.Run, snapshot task.Task) error {
	dir, err := s.runDir(r.ID)
	if err != nil {
		return fmt.Errorf("store: invalid run id %q", r.ID)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "task.json"), snapshot); err != nil {
		return err
	}
	return s.writeRun(dir, r)
}

// writeRun 写运行的元数据并更新内存索引。所有改写 run.json 的地方都经过这里。
func (s *Store) writeRun(dir string, r task.Run) error {
	if err := writeJSON(filepath.Join(dir, "run.json"), r); err != nil {
		return err
	}
	s.index(r)
	return nil
}

// FinishRun 保存运行的输出和最终元数据(O1)。先写输出再写元数据,
// 这样元数据显示为终态时输出一定已经落盘。
func (s *Store) FinishRun(r task.Run) error {
	dir, err := s.runDir(r.ID)
	if err != nil {
		return fmt.Errorf("store: invalid run id %q", r.ID)
	}
	if err := writeFile(filepath.Join(dir, "stdout.log"), []byte(r.Stdout)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "stderr.log"), []byte(r.Stderr)); err != nil {
		return err
	}
	return s.writeRun(dir, r)
}

// UpdateRun 只改写运行的元数据,用于已经结束的运行(如标为已读)。
func (s *Store) UpdateRun(r task.Run) error {
	dir, err := s.runDir(r.ID)
	if err != nil {
		return ErrNotFound
	}
	return s.writeRun(dir, r)
}

// SaveDelivery 保存一条投递记录,已存在则覆盖。
func (s *Store) SaveDelivery(d task.Delivery) error {
	dir, err := s.runDir(d.RunID)
	if err != nil {
		return ErrNotFound
	}
	dir = filepath.Join(dir, "deliveries")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, strconv.Itoa(d.Index)+".json"), d)
}

// Deliveries 返回一次运行的投递记录,按通知设置的顺序。
func (s *Store) Deliveries(runID string) ([]task.Delivery, error) {
	dir, err := s.runDir(runID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "deliveries"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var list []task.Delivery
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var d task.Delivery
		if err := readJSON(filepath.Join(dir, "deliveries", e.Name()), &d); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	slices.SortFunc(list, func(a, b task.Delivery) int { return a.Index - b.Index })
	return list, nil
}

// PendingDeliveries 返回全部等待投递或重试的记录。后台启动时用它接着投递。
func (s *Store) PendingDeliveries() ([]task.Delivery, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "runs", "*", "deliveries"))
	if err != nil {
		return nil, err
	}
	var pending []task.Delivery
	for _, m := range matches {
		list, err := s.Deliveries(filepath.Base(filepath.Dir(m)))
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			if d.Status == task.DeliveryPending {
				pending = append(pending, d)
			}
		}
	}
	return pending, nil
}

// LoadRun 返回运行的元数据,不含输出。
func (s *Store) LoadRun(id string) (task.Run, error) {
	var r task.Run
	dir, err := s.runDir(id)
	if err != nil {
		return r, err
	}
	return r, readJSON(filepath.Join(dir, "run.json"), &r)
}

// LoadOutput 返回运行的标准输出和标准错误。尚未结束的运行返回空内容。
func (s *Store) LoadOutput(id string) (stdout, stderr string, err error) {
	dir, err := s.runDir(id)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(dir); err != nil {
		return "", "", ErrNotFound
	}
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
	if stdout, err = read("stdout.log"); err != nil {
		return "", "", err
	}
	stderr, err = read("stderr.log")
	return stdout, stderr, err
}

// Snapshot 返回运行开始时的任务配置。
func (s *Store) Snapshot(id string) (task.Task, error) {
	var t task.Task
	dir, err := s.runDir(id)
	if err != nil {
		return t, err
	}
	return t, readJSON(filepath.Join(dir, "task.json"), &t)
}

// ListRuns 返回运行记录,最新的在前。taskName 为空表示全部任务,limit 不大于 0 表示不限数量。
// 先在索引里筛出编号,只读取要返回的那几条的 run.json。
func (s *Store) ListRuns(taskName string, limit int) ([]task.Run, error) {
	var ids []string
	err := s.EachRun(func(e RunEntry) bool {
		if taskName == "" || e.Task == taskName {
			ids = append(ids, e.ID)
		}
		return limit <= 0 || len(ids) < limit
	})
	if err != nil {
		return nil, err
	}
	return s.LoadRuns(ids)
}

// InterruptUnfinished 把停留在排队或运行中的记录标为中断(O3)。
// 后台启动时调用:此时这些记录对应的进程已经不归任何人管理。
func (s *Store) InterruptUnfinished(now time.Time) (int, error) {
	var ids []string
	if err := s.EachRun(func(e RunEntry) bool {
		if !e.Status.Finished() {
			ids = append(ids, e.ID)
		}
		return true
	}); err != nil {
		return 0, err
	}
	runs, err := s.LoadRuns(ids)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Status.Finished() {
			continue
		}
		r.Status, r.FinishedAt, r.Error = task.Interrupted, now, i18n.T("后台在这次运行结束前退出")
		dir, _ := s.runDir(r.ID)
		if err := s.writeRun(dir, r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// LoadState 返回每个任务已评估到的时间点(S7)。文件不存在时返回空表。
//
// 文件内容损坏(比如断电时正写到一半)时改名为 state.json.broken 留作查看,返回空的进度和 ErrStateBroken。
// 没有进度的任务从现在开始计时,所以不会重复触发;代价是这段时间里错过的触发不补跑。
func (s *Store) LoadState() (map[string]time.Time, error) {
	path := filepath.Join(s.dir, "state.json")
	state := map[string]time.Time{}
	err := readJSON(path, &state)
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case err == nil:
		return state, nil
	case errors.Is(err, ErrNotFound):
		return state, nil
	case errors.As(err, &syntax), errors.As(err, &typ), errors.Is(err, io.ErrUnexpectedEOF):
		if renameErr := os.Rename(path, path+".broken"); renameErr != nil {
			return nil, renameErr
		}
		return map[string]time.Time{}, fmt.Errorf("%w: %v", ErrStateBroken, err)
	default:
		return nil, err
	}
}

// ErrStateBroken 表示调度进度文件已损坏,已经移开,进度从空开始。
var ErrStateBroken = errors.New("store: state.json is broken")

// SaveRunnerConfig 把一份运行器配置写进 runners/<名字>.json,同名的替换。
func (s *Store) SaveRunnerConfig(name string, data []byte) error {
	dir := s.RunnersDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, filepath.Base(name)+".json"), append(data, '\n'))
}

// Dir 返回数据目录。
func (s *Store) Dir() string { return s.dir }

// StoredToken 是 tokens.json 里的一条:只有哈希,明文不落盘(决策记录 0015)。
type StoredToken struct {
	Name      string    `json:"name"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

// LoadTokens 读取另外创建的访问令牌,文件不存在时返回空表。
func (s *Store) LoadTokens() ([]StoredToken, error) {
	list := []StoredToken{}
	err := readJSON(filepath.Join(s.dir, "tokens.json"), &list)
	if errors.Is(err, ErrNotFound) {
		return []StoredToken{}, nil
	}
	return list, err
}

// SaveTokens 保存另外创建的访问令牌,文件只有当前用户可读。
func (s *Store) SaveTokens(list []StoredToken) error {
	return writeJSON(filepath.Join(s.dir, "tokens.json"), list)
}

// LoadSettings 把设置读进 v,还没有设置过时不改动 v。
func (s *Store) LoadSettings(v any) error {
	err := readJSON(filepath.Join(s.dir, "settings.json"), v)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// SaveSettings 保存设置。
func (s *Store) SaveSettings(v any) error { return writeJSON(filepath.Join(s.dir, "settings.json"), v) }

func (s *Store) SaveState(state map[string]time.Time) error {
	return writeJSON(filepath.Join(s.dir, "state.json"), state)
}

// Secret 返回数据目录下名为 name 的凭证文件的内容;文件不存在时生成随机值并保存,
// 文件仅当前用户可读(C2)。
func (s *Store) Secret(name string) (string, error) {
	path := filepath.Join(s.dir, filepath.Base(name))
	b, err := os.ReadFile(path)
	if err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, os.Chmod(path, 0o600)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	v := hex.EncodeToString(raw[:])
	return v, writeFile(path, []byte(v+"\n"))
}

// WritePrivate 在数据目录下写入一个仅当前用户可读的文件。
func (s *Store) WritePrivate(name string, data []byte) error {
	return writeFile(filepath.Join(s.dir, filepath.Base(name)), data)
}

// Remove 删除数据目录下的一个文件,文件不存在不算错误。
func (s *Store) Remove(name string) error {
	err := os.Remove(filepath.Join(s.dir, filepath.Base(name)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Lock 独占数据目录,保证同一份数据只有一个后台在使用。返回的函数用于释放。
func (s *Store) Lock() (unlock func(), err error) { return lock(filepath.Join(s.dir, "lock")) }

// ErrLocked 表示数据目录正被另一个后台使用。
var ErrLocked = errors.New("store: data directory is in use by another daemon")

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'))
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeFile 先写临时文件再改名,读的一方不会看到写了一半的内容。
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// 先落盘再改名:断电后看到的要么是旧内容,要么是完整的新内容,而不是一个空文件。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // CreateTemp 建出的文件权限为 0600
}
