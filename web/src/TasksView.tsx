import { useEffect, useState } from "react";
import { api, taskPath, type Task } from "./api";
import { describeTrigger, formatTime } from "./format";
import { Button, Mark } from "./ui";

interface Props {
  tick: number;
  onLoaded: () => void;
  onError: (err: unknown) => void;
  // act 执行一个操作,出错时显示后台的说明,之后刷新。
  act: (fn: () => Promise<unknown>) => void;
  onNew: () => void;
  onEdit: (t: Task) => void;
  onRunStarted: (runId: string) => void;
}

export function TasksView({ tick, onLoaded, onError, act, onNew, onEdit, onRunStarted }: Props) {
  const [tasks, setTasks] = useState<Task[] | null>(null);

  useEffect(() => {
    let live = true;
    api<Task[]>("GET", "/tasks").then(
      (data) => {
        if (!live) return;
        setTasks(data);
        onLoaded();
      },
      (err) => live && onError(err),
    );
    return () => {
      live = false;
    };
    // 只在定期刷新或操作之后重新读取
  }, [tick]);

  return (
    <section>
      <div className="head">
        <h2>任务</h2>
        <Button variant="primary" onClick={onNew}>
          新建任务
        </Button>
      </div>
      {tasks && tasks.length === 0 && <p className="empty">还没有任务。点"新建任务",让一条命令、一个脚本或一个 AI 命令行按时运行。</p>}
      {tasks && tasks.length > 0 && (
        <div className="scroll">
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>触发</th>
                <th>状态</th>
                <th>下次运行</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((t) => (
                <tr key={t.name}>
                  <td className="mono">{t.name}</td>
                  <td className="time">{describeTrigger(t.trigger)}</td>
                  <td>
                    {t.running ? <Mark kind="spin ink">运行中</Mark> : t.paused ? <Mark kind="ring quiet">已暂停</Mark> : <Mark kind="ring">等待</Mark>}
                  </td>
                  <td className="time">{formatTime(t.next_run)}</td>
                  <td>
                    <div className="ops">
                      <Button onClick={() => act(async () => onRunStarted((await api<{ id: string }>("POST", taskPath(t.name, "run"))).id))}>
                        立即运行
                      </Button>
                      <Button onClick={() => act(() => api("POST", taskPath(t.name, t.paused ? "resume" : "pause")))}>{t.paused ? "恢复" : "暂停"}</Button>
                      <Button onClick={() => onEdit(t)}>编辑</Button>
                      <Button
                        variant="danger"
                        onClick={() => {
                          if (confirm(`删除任务 ${t.name}?运行记录会保留。`)) act(() => api("DELETE", taskPath(t.name)));
                        }}
                      >
                        删除
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
