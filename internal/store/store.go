package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugu/internal/task"
)

// ErrNotFound 表示请求的任务或运行不存在。
var ErrNotFound = errors.New("store: not found")

// 数据目录的布局:
//
//	tasks/<任务名>.json       任务定义,一个任务一个文件
//	runs/<运行编号>/run.json   运行的元数据
//	runs/<运行编号>/task.json  运行开始时的任务配置快照
//	runs/<运行编号>/stdout.log、stderr.log
//	state.json                每个任务已评估到的时间点
type Store struct {
	dir string

	mu        sync.Mutex
	idLoaded  bool
	lastStamp string // 最近一个运行编号的时间部分
	lastSeq   int    // 以及序号部分
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
			return nil, fmt.Errorf("读取任务文件 %s: %w", e.Name(), err)
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
	return writeJSON(filepath.Join(dir, "run.json"), r)
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
	return writeJSON(filepath.Join(dir, "run.json"), r)
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
func (s *Store) ListRuns(taskName string, limit int) ([]task.Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil {
		return nil, err
	}
	var runs []task.Run
	for _, e := range slices.Backward(entries) { // ReadDir 按名字升序,即按时间升序
		if limit > 0 && len(runs) == limit {
			break
		}
		r, err := s.LoadRun(e.Name())
		if errors.Is(err, ErrNotFound) {
			continue // 不是运行目录,或后台在建目录和写元数据之间退出
		} else if err != nil {
			return nil, fmt.Errorf("读取运行记录 %s: %w", e.Name(), err)
		}
		if taskName == "" || r.Task == taskName {
			runs = append(runs, r)
		}
	}
	return runs, nil
}

// InterruptUnfinished 把停留在排队或运行中的记录标为中断(O3)。
// 后台启动时调用:此时这些记录对应的进程已经不归任何人管理。
func (s *Store) InterruptUnfinished(now time.Time) (int, error) {
	runs, err := s.ListRuns("", 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Status.Finished() {
			continue
		}
		r.Status, r.FinishedAt, r.Error = task.Interrupted, now, "后台在这次运行结束前退出"
		dir, _ := s.runDir(r.ID)
		if err := writeJSON(filepath.Join(dir, "run.json"), r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// LoadState 返回每个任务已评估到的时间点(S7)。文件不存在时返回空表。
func (s *Store) LoadState() (map[string]time.Time, error) {
	state := map[string]time.Time{}
	err := readJSON(filepath.Join(s.dir, "state.json"), &state)
	if errors.Is(err, ErrNotFound) {
		return state, nil
	}
	return state, err
}

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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // CreateTemp 建出的文件权限为 0600
}
