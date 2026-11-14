package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"bugu/internal/i18n"
	"bugu/internal/task"
)

// RunEntry 是运行记录在内存索引里的样子:只有筛选和计数要用的几项,完整的元数据仍在 run.json。
// 收件箱的计数、按任务查询、上次成功时间都只看索引,不必每次把全部 run.json 读一遍。
type RunEntry struct {
	ID        string
	Task      string
	Status    task.Status
	StartedAt time.Time
	InInbox   bool // 见 task.Run.InInbox
	Unread    bool // 见 task.Run.Unread
}

func entryOf(r task.Run) RunEntry {
	return RunEntry{ID: r.ID, Task: r.Task, Status: r.Status, StartedAt: r.StartedAt, InInbox: r.InInbox(), Unread: r.Unread()}
}

// runIndex 按编号升序(即创建顺序)保存全部运行的 RunEntry。第一次用到时读一遍运行目录建立,
// 之后每次经 Store 写 run.json 时同步更新。数据目录由一个后台独占,所以不会有别人绕过它改写。
type runIndex struct {
	mu      sync.Mutex
	loaded  bool
	entries []RunEntry
}

// loadIndexLocked 在第一次使用时读取全部运行目录。调用方持有 mu。
func (s *Store) loadIndexLocked() error {
	ix := &s.runs
	if ix.loaded {
		return nil
	}
	dirs, err := os.ReadDir(filepath.Join(s.dir, "runs"))
	if err != nil {
		return err
	}
	entries := make([]RunEntry, 0, len(dirs))
	for _, d := range dirs { // ReadDir 按名字升序,即按创建顺序
		if !runIDPattern.MatchString(d.Name()) {
			continue
		}
		r, err := s.LoadRun(d.Name())
		if errors.Is(err, ErrNotFound) {
			continue // 后台在建目录和写元数据之间退出
		} else if err != nil {
			return i18n.Errorf("读取运行记录 %s: %w", d.Name(), err)
		}
		entries = append(entries, entryOf(r))
	}
	ix.entries, ix.loaded = entries, true
	return nil
}

// index 在 run.json 写好之后更新索引。索引还没建立时什么也不做,建立时会读到这次写下的内容。
func (s *Store) index(r task.Run) {
	ix := &s.runs
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		return
	}
	e := entryOf(r)
	i, found := slices.BinarySearchFunc(ix.entries, r.ID, func(e RunEntry, id string) int { return strings.Compare(e.ID, id) })
	if found {
		ix.entries[i] = e
	} else {
		ix.entries = slices.Insert(ix.entries, i, e)
	}
}

// EachRun 从新到旧依次把运行记录的索引交给 fn,fn 返回 false 时停止。
// fn 运行时持有索引的锁,不能在里面调用 Store 写运行记录的方法;要改写的先记下编号,遍历完再改。
func (s *Store) EachRun(fn func(RunEntry) bool) error {
	ix := &s.runs
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := s.loadIndexLocked(); err != nil {
		return err
	}
	for _, e := range slices.Backward(ix.entries) {
		if !fn(e) {
			break
		}
	}
	return nil
}

// LoadRuns 按给出的编号依次读取完整的运行元数据。读的时候已被删除的跳过。
func (s *Store) LoadRuns(ids []string) ([]task.Run, error) {
	runs := make([]task.Run, 0, len(ids))
	for _, id := range ids {
		r, err := s.LoadRun(id)
		if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return nil, i18n.Errorf("读取运行记录 %s: %w", id, err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}
