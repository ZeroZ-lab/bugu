import { Fragment, useEffect, useState } from "react";
import { api, runPath, type Logs, type Run } from "./api";
import { formatTime, PERMISSION, statusOf } from "./format";
import { Button, Modal } from "./ui";

interface Props {
  id: string;
  tick: number;
  onClose: () => void;
  onError: (err: unknown) => void;
}

// RunDialog 显示一次运行的详情和输出。运行还没结束时跟着定期刷新更新。
export function RunDialog({ id, tick, onClose, onError }: Props) {
  const [data, setData] = useState<{ run: Run; logs: Logs } | null>(null);
  const done = data !== null && data.run.status !== "queued" && data.run.status !== "running";

  useEffect(() => {
    if (done) return;
    let live = true;
    Promise.all([api<Run>("GET", runPath(id)), api<Logs>("GET", runPath(id, "logs"))]).then(
      ([run, logs]) => live && setData({ run, logs }),
      (err) => {
        if (!live) return;
        onError(err);
        if (!data) onClose();
      },
    );
    return () => {
      live = false;
    };
  }, [id, tick]);

  if (!data) return null;
  const { run: r, logs } = data;
  const rows: [string, string][] = [
    ["运行编号", r.id],
    ["开始", formatTime(r.started_at || r.created_at)],
    ["结束", formatTime(r.finished_at)],
    ["退出码", r.exit_code === undefined ? "-" : String(r.exit_code)],
    ["命令", (r.command ?? []).join(" ") || "-"],
  ];
  if (r.permission) rows.push(["权限", PERMISSION[r.permission] ?? r.permission]);
  if (r.scheduled_for) rows.push(["原定时间", formatTime(r.scheduled_for)]);
  if (r.note) rows.push(["说明", r.note]);
  if (r.error) rows.push(["原因", r.error]);

  return (
    <Modal wide onClose={onClose}>
      <div className="head">
        <h2>
          {r.task} · {statusOf(r.status)[0]}
        </h2>
        <Button onClick={onClose}>关闭</Button>
      </div>
      <dl>
        {rows.map(([k, v]) => (
          <Fragment key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </Fragment>
        ))}
      </dl>
      <h3>标准输出</h3>
      <pre>{logs.stdout}</pre>
      <h3>标准错误</h3>
      <pre>{logs.stderr}</pre>
    </Modal>
  );
}
