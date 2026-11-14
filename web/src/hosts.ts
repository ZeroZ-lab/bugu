// 登记的其他主机(G5)在页面上的读取。登记表里有那些主机的令牌,只有本机令牌能读(决策记录 0015)。
import { api, ApiError, type Host } from "./api";

// Hosts 保存读到的登记表。内容变化时调用 changed,由使用它的元素重新渲染。
export class Hosts {
  hosts: Host[] | null = null; // null 表示还没读到
  forbidden = false; // 不是用本机令牌打开的页面,读不到登记表
  error = "";

  constructor(private readonly changed: () => void) {}

  // reload 先不检查连接状态,马上列出主机;检查要等每台主机回应,完成后再补上状态。
  reload = () => {
    const fail = (err: unknown) => {
      if (err instanceof ApiError && err.code === "forbidden") this.forbidden = true;
      else this.error = err instanceof Error ? err.message : String(err);
      this.changed();
    };
    let probed = false;
    api<Host[]>("GET", "/hosts?probe=0").then((list) => {
      if (probed) return;
      this.hosts = list;
      this.changed();
    }, fail);
    api<Host[]>("GET", "/hosts").then((list) => {
      probed = true;
      this.hosts = list;
      this.error = "";
      this.changed();
    }, fail);
  };
}

// 连不上或令牌被拒时打开只会停在错误页,所以不让打开。
export const reachable = (h: Host) => h.status !== "offline" && h.status !== "unauthorized";
