import { useEffect, useState } from "react";
import { api, type Run, type Task } from "./api";
import { formatTime, statusOf } from "./format";
import { Mark } from "./ui";

interface Props {
  tick: number;
  onLoaded: () => void;
  onError: (err: unknown) => void;
  onOpen: (runId: string) => void;
}

export function RunsView({ tick, onLoaded, onError, onOpen }: Props) {
  const [taskNames, setTaskNames] = useState<string[]>([]);
  const [filter, setFilter] = useState("");
  const [runs, setRuns] = useState<Run[] | null>(null);

  useEffect(() => {
    let live = true;
    const query = new URLSearchParams({ limit: "100" });
    if (filter) query.set("task", filter);
    Promise.all([api<Task[]>("GET", "/tasks"), api<Run[]>("GET", "/runs?" + query)]).then(
      ([tasks, data]) => {
        if (!live) return;
        const names = tasks.map((t) => t.name);
        setTaskNames(names);
        if (filter && !names.includes(filter)) setFilter("");
        setRuns(data);
        onLoaded();
      },
      (err) => live && onError(err),
    );
    return () => {
      live = false;
    };
  }, [tick, filter]);

  return (
    <section>
      <div className="head">
        <h2>运行记录</h2>
        <label className="inline">
          任务{" "}
          <select value={filter} onChange={(e) => setFilter(e.target.value)}>
            <option value="">全部</option>
            {taskNames.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
      </div>
      {runs && runs.length === 0 && <p className="empty">还没有运行记录。</p>}
      {runs && runs.length > 0 && (
        <div className="scroll">
          <table className="clickable">
            <thead>
              <tr>
                <th>开始时间</th>
                <th>任务</th>
                <th>状态</th>
                <th>退出码</th>
                <th>来源</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => {
                const [label, kind] = statusOf(r.status);
                return (
                  <tr key={r.id} tabIndex={0} onClick={() => onOpen(r.id)} onKeyDown={(e) => e.key === "Enter" && onOpen(r.id)}>
                    <td className="time">{formatTime(r.started_at || r.created_at)}</td>
                    <td className="mono">{r.task}</td>
                    <td>
                      <Mark kind={kind}>{label}</Mark>
                    </td>
                    <td>{r.exit_code === undefined ? "-" : String(r.exit_code)}</td>
                    <td>{r.trigger === "manual" ? "手动" : "定时"}</td>
                    <td className="wrap">{r.note || (r.status === "failed" ? "" : r.error || "")}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
