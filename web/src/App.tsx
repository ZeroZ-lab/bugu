// 布谷的网页前端。只通过后台的 JSON 接口工作,应用窗口和浏览器里是同一份。
// 来自后台的内容都作为文本渲染,不拼接 HTML。
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, clearToken, getToken, setToken, takeRunFromHash, takeTokenFromHash, type Runner, type Task } from "./api";
import { RunDialog } from "./RunDialog";
import { RunsView } from "./RunsView";
import { TaskDialog } from "./TaskDialog";
import { TasksView } from "./TasksView";
import { Button, ErrorText, Field, Logo } from "./ui";

const REFRESH_MS = 5000;

type View = "tasks" | "runs";

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));

export function App() {
  const [authed, setAuthed] = useState(() => {
    takeTokenFromHash();
    return getToken() !== "";
  });
  const [loginError, setLoginError] = useState("");
  const [view, setView] = useState<View>("tasks");
  const [pageError, setPageError] = useState("");
  const [connected, setConnected] = useState(false);
  const [tick, setTick] = useState(0);
  const [runId, setRunId] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ task: Task | null; runners: Runner[] } | null>(null);

  const refresh = useCallback(() => setTick((n) => n + 1), []);

  // 令牌失效时回到连接页;其他错误显示在页面上。
  const onError = useCallback((err: unknown) => {
    if (err instanceof ApiError && err.code === "unauthorized") {
      clearToken();
      setAuthed(false);
      setLoginError("令牌无效,请重新填写。");
      return;
    }
    setConnected(false);
    setPageError(message(err));
  }, []);

  const onLoaded = useCallback(() => {
    setConnected(true);
    setPageError("");
  }, []);

  const act = useCallback(
    (fn: () => Promise<unknown>) => {
      fn().then(refresh, onError);
    },
    [refresh, onError],
  );

  function showView(v: View) {
    setView(v);
    setPageError("");
  }

  function openRun(id: string) {
    showView("runs");
    setRunId(id);
  }

  // 桌面应用会通过网址片段送来令牌(#token=)和要打开的运行(#run=,点通知时)。
  useEffect(() => {
    function readHash() {
      takeTokenFromHash();
      if (!getToken()) return;
      setAuthed(true);
      const id = takeRunFromHash();
      if (id) openRun(id);
    }
    readHash();
    window.addEventListener("hashchange", readHash);
    return () => window.removeEventListener("hashchange", readHash);
  }, []);

  // 页面可见且没有在编辑任务时定期刷新,让状态和下次运行时间保持最新。
  useEffect(() => {
    if (!authed || editing) return;
    const timer = setInterval(() => {
      if (!document.hidden) refresh();
    }, REFRESH_MS);
    return () => clearInterval(timer);
  }, [authed, editing, refresh]);

  async function openTaskDialog(task: Task | null) {
    try {
      const runners = await api<Runner[]>("GET", "/runners");
      setEditing({ task, runners });
    } catch (err) {
      onError(err);
    }
  }

  async function login(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const input = e.currentTarget.elements.namedItem("token") as HTMLInputElement;
    setToken(input.value.trim());
    try {
      await api("GET", "/health");
      input.value = "";
      setLoginError("");
      setAuthed(true);
      showView("tasks");
      refresh();
    } catch (err) {
      clearToken();
      setLoginError(err instanceof ApiError && err.code === "unauthorized" ? "令牌无效。" : message(err));
    }
  }

  return (
    <>
      <header className="bar">
        <h1 className="brand" aria-label="布谷">
          <Logo />
          bugu
        </h1>
        {authed && (
          <nav>
            {(["tasks", "runs"] as const).map((v) => (
              <button key={v} type="button" className={"tab" + (view === v ? " active" : "")} onClick={() => showView(v)}>
                {v === "tasks" ? "任务" : "运行记录"}
              </button>
            ))}
          </nav>
        )}
        <span className="spacer" />
        <span className="muted conn">{authed && connected ? "已连接 " + location.host : ""}</span>
      </header>

      <main>
        {!authed ? (
          <section>
            <form className="card narrow" onSubmit={login}>
              <h2>连接后台</h2>
              <p className="muted">
                粘贴这台主机的访问令牌。本机的令牌保存在数据目录的 <code>token</code> 文件里,默认是 <code>~/.bugu/token</code>。
              </p>
              <Field label="令牌">
                <input name="token" type="password" autoComplete="off" required />
              </Field>
              <ErrorText message={loginError} />
              <Button type="submit" variant="primary">
                连接
              </Button>
            </form>
          </section>
        ) : view === "tasks" ? (
          <TasksView
            tick={tick}
            onLoaded={onLoaded}
            onError={onError}
            act={act}
            onNew={() => openTaskDialog(null)}
            onEdit={(t) => openTaskDialog(t)}
            onRunStarted={openRun}
          />
        ) : (
          <RunsView tick={tick} onLoaded={onLoaded} onError={onError} onOpen={openRun} />
        )}
        {authed && <ErrorText message={pageError} />}
      </main>

      {editing && (
        <TaskDialog
          task={editing.task}
          runners={editing.runners}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            refresh();
          }}
        />
      )}
      {authed && runId && <RunDialog key={runId} id={runId} tick={tick} onClose={() => setRunId(null)} onError={onError} />}
    </>
  );
}
