// 任务列表按工作目录分组(G1):同一个项目的几个任务放在一起,不平铺成一长串。
// 只在界面上分,任务本身没有"分组"字段;没填工作目录的任务归到最后一组。

export interface TaskGroup<T> {
  key: string; // 规整后的目录,没有目录时为空串
  label: string; // 组名:目录的最后一段,重名时多带一段上级目录
  tasks: T[];
}

const trimDir = (dir: string | undefined) => (dir ?? "").trim().replace(/(.)\/+$/, "$1");

const tail = (dir: string, n: number) => dir.split("/").filter(Boolean).slice(-n).join("/") || dir;

// groupByDir 按工作目录分组。组按组名排序,没有目录的一组放最后;组内保持原来的顺序。
export function groupByDir<T extends { cwd?: string }>(tasks: T[]): TaskGroup<T>[] {
  const byKey = new Map<string, T[]>();
  for (const task of tasks) {
    const key = trimDir(task.cwd);
    const list = byKey.get(key);
    if (list) list.push(task);
    else byKey.set(key, [task]);
  }
  const keys = [...byKey.keys()].filter(Boolean);
  // 组名先取最后一段;和别的组重名就逐段往上加,直到分得开。
  const label = (key: string) => {
    for (let n = 1; ; n++) {
      const mine = tail(key, n);
      if (mine === key || !keys.some((k) => k !== key && tail(k, n) === mine)) return mine;
    }
  };
  const groups = keys.map((key) => ({ key, label: label(key), tasks: byKey.get(key)! }));
  groups.sort((a, b) => a.label.localeCompare(b.label) || a.key.localeCompare(b.key));
  const rest = byKey.get("");
  if (rest) groups.push({ key: "", label: "", tasks: rest });
  return groups;
}

// 折起来的组只记在这台设备上,和外观设置一样。
const COLLAPSED_KEY = "bugu.collapsedGroups";

export function getCollapsed(): Set<string> {
  try {
    const v = JSON.parse(localStorage.getItem(COLLAPSED_KEY) ?? "[]");
    return new Set(Array.isArray(v) ? v.filter((x) => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export function setCollapsed(keys: Set<string>) {
  try {
    if (keys.size === 0) localStorage.removeItem(COLLAPSED_KEY);
    else localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...keys]));
  } catch {
    // 存不了就只在这次打开时生效
  }
}
