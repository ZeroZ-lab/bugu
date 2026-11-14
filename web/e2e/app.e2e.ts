// 网页前端的主要流程,对真实后台运行。只用脚本任务,不调用任何 AI 命令行。
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";

const base = () => process.env.BUGU_URL!;
const token = () => process.env.BUGU_TOKEN!;
const auth = () => ({ Authorization: "Bearer " + token() });

async function createTask(request: APIRequestContext, name: string, command: string) {
  const resp = await request.post(base() + "/api/v1/tasks", {
    headers: auth(),
    data: { name, trigger: { cron: "0 9 * * *" }, steps: [{ type: "script", script: { command } }] },
  });
  expect(resp.ok(), await resp.text()).toBe(true);
}

async function runTask(request: APIRequestContext, name: string): Promise<string> {
  const resp = await request.post(`${base()}/api/v1/tasks/${name}/run`, { headers: auth() });
  expect(resp.ok(), await resp.text()).toBe(true);
  const { id } = (await resp.json()) as { id: string };
  await expect.poll(async () => (await (await request.get(`${base()}/api/v1/runs/${id}`, { headers: auth() })).json()).status).toBe("succeeded");
  return id;
}

async function getTask(request: APIRequestContext, name: string) {
  return (await request.get(`${base()}/api/v1/tasks/${name}`, { headers: auth() })).json();
}

// 桌面应用打开窗口时就是这样把令牌交给页面的(决策记录 0007)。
async function openApp(page: Page) {
  await page.goto(`${base()}/#token=${token()}`);
  await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();
}

// "命令"这个字也出现在"一条命令"单选项里,所以按文本框找。
const commandBox = (form: Locator) => form.getByRole("textbox", { name: /^命令/ });

// 新建和修改任务的页面上的表单,以页面标题为名。
const taskForm = (page: Page) => page.getByRole("form", { name: /^(新建任务|修改任务|New task|Edit task)$/ });

// 任务名带上本次重复的序号,同一个后台上重复运行测试时不会撞名。
const n = (name: string) => `e2e-${name}-${test.info().repeatEachIndex}-${test.info().retry}`;

const row = (page: Page, name: string) => page.getByRole("row").filter({ hasText: name });

// 决策记录 0007:页面本身不需要令牌,令牌由用户粘贴;错的令牌留在连接页并说明。
test("连接页:错误的令牌给出说明,正确的令牌进入任务列表", async ({ page }) => {
  await page.goto(base() + "/");
  await page.getByLabel("令牌").fill("not-the-token");
  await page.getByRole("button", { name: "连接" }).click();
  await expect(page.getByRole("alert")).toHaveText("令牌无效。");

  await page.getByLabel("令牌").fill(token());
  await page.getByRole("button", { name: "连接" }).click();
  await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();
  await expect(page.getByText("已连接")).toBeVisible();
});

// G2 新建任务,G1 立即运行,G4 单次运行的完整输出。
test("G2 G1 G4:用表单新建脚本任务,立即运行并看到输出", async ({ page }) => {
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await expect(form.getByRole("heading", { name: "新建任务" })).toBeVisible();
  await form.getByLabel("名称").fill(n("hello"));
  await commandBox(form).fill("echo hello-from-e2e");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();

  const r = row(page, n("hello"));
  await expect(r).toContainText("等待");
  await r.getByRole("button", { name: "立即运行" }).click();

  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${n("hello")} · 成功` })).toBeVisible({ timeout: 15_000 });
  await expect(details.locator("pre").first()).toHaveText("hello-from-e2e\n");
  await details.getByRole("button", { name: "关闭" }).click();
  await expect(details).toBeHidden();
});

// G2:表单不合规时不提交,留在表单页上。
test("G2:名称不合规时浏览器拦下表单,任务没有建出来", async ({ page, request }) => {
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await form.getByLabel("名称").fill("有空格 的名字");
  await commandBox(form).fill("true");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeVisible();
  expect(await form.getByLabel("名称").evaluate((el: HTMLInputElement) => el.validity.patternMismatch)).toBe(true);
  page.once("dialog", (d) => d.accept());
  await form.getByRole("button", { name: "取消" }).click();
  await expect(form).toBeHidden();
  const tasks = (await (await request.get(base() + "/api/v1/tasks", { headers: auth() })).json()) as unknown[];
  expect(tasks.some((t) => (t as { name: string }).name.includes(" "))).toBe(false);
});

// G1:暂停和恢复。
// G1:应用窗口里没有浏览器的刷新键,顶栏的"刷新"立即读回别处做的改动。
// 时钟停住,定时刷新不会发生,看到的新任务只能来自这个按钮。
test("G1:顶栏的刷新按钮立即读回命令行新建的任务", async ({ page, request }) => {
  await page.clock.install();
  await openApp(page);
  await page.clock.pauseAt(new Date(Date.now() + 1000));
  await createTask(request, n("refresh"), "true");
  await expect(row(page, n("refresh"))).toHaveCount(0);

  const refresh = page.getByRole("button", { name: "刷新" });
  await refresh.click();
  await expect(row(page, n("refresh"))).toBeVisible();
  await expect(refresh).toBeEnabled();

  // 新建、修改任务的页面上不放刷新,免得冲掉正在填的内容。
  await page.getByRole("button", { name: "新建任务" }).click();
  await expect(taskForm(page)).toBeVisible();
  await expect(refresh).toBeHidden();
});

test("G1:任务按工作目录分组,组可以折起,重新打开仍是折起的", async ({ page, request }) => {
  const add = async (name: string, cwd: string, command: string) => {
    const resp = await request.post(base() + "/api/v1/tasks", {
      headers: auth(),
      data: { name, cwd, trigger: { cron: "0 9 * * *" }, steps: [{ type: "script", script: { command } }] },
    });
    expect(resp.ok(), await resp.text()).toBe(true);
  };
  // 目录名各次运行都不同,组名就是目录的最后一段,不会和别的组重名。
  const dirA = "/tmp/" + n("dir-alpha");
  const dirB = "/tmp/" + n("dir-beta");
  await add(n("grp-a1"), dirA, "true");
  await add(n("grp-a2"), dirA, "true");
  await add(n("grp-b1"), dirB, "exit 3");
  const resp = await request.post(`${base()}/api/v1/tasks/${n("grp-b1")}/run`, { headers: auth() });
  const { id } = (await resp.json()) as { id: string };
  await expect.poll(async () => (await (await request.get(`${base()}/api/v1/runs/${id}`, { headers: auth() })).json()).status).not.toMatch(/^(queued|running)$/);

  await openApp(page);
  const groupA = page.getByRole("button", { name: new RegExp("^" + n("dir-alpha")) });
  const groupB = page.getByRole("button", { name: new RegExp("^" + n("dir-beta")) });
  await expect(groupA).toHaveAttribute("aria-expanded", "true");
  await expect(groupA).toContainText("2");
  // 组里有上次失败的任务,组头直接标出来,折起来也看得见。
  await expect(groupB).toContainText("1 个失败");
  await expect(groupA).not.toContainText("失败");

  await groupA.click();
  await expect(groupA).toHaveAttribute("aria-expanded", "false");
  await expect(row(page, n("grp-a1"))).toHaveCount(0);
  await expect(row(page, n("grp-a2"))).toHaveCount(0);
  await expect(row(page, n("grp-b1"))).toBeVisible();

  await openApp(page);
  await expect(groupA).toHaveAttribute("aria-expanded", "false");
  await expect(row(page, n("grp-a1"))).toHaveCount(0);

  await groupA.click();
  await expect(row(page, n("grp-a1"))).toBeVisible();
  await expect(row(page, n("grp-a2"))).toBeVisible();
});

test("G1:暂停后显示已暂停,恢复后回到等待", async ({ page, request }) => {
  await createTask(request, n("pause"), "true");
  await openApp(page);
  const r = row(page, n("pause"));
  await r.getByRole("button", { name: "暂停" }).click();
  await expect(r).toContainText("已暂停");
  expect((await getTask(request, n("pause"))).paused).toBe(true);

  await r.getByRole("button", { name: "恢复" }).click();
  await expect(r).toContainText("等待");
  expect((await getTask(request, n("pause"))).paused ?? false).toBe(false);
});

// G2:修改时回填原值,名称不能改,保存后后台的定义随之更新。
test("G2:修改任务回填原值,保存后生效", async ({ page, request }) => {
  await createTask(request, n("edit"), "echo before");
  await openApp(page);
  await row(page, n("edit")).getByRole("button", { name: "编辑" }).click();

  const form = taskForm(page);
  await expect(form.getByRole("heading", { name: "修改任务" })).toBeVisible();
  await expect(form.getByLabel("名称")).toHaveValue(n("edit"));
  await expect(form.getByLabel("名称")).toBeDisabled();
  const command = commandBox(form);
  await expect(command).toHaveValue("echo before");
  await command.fill("echo after");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();

  expect((await getTask(request, n("edit"))).steps[0].script.command).toBe("echo after");
});

// G2:新建和修改任务是单独的页面,有自己的地址;取消、保存、浏览器后退都回到任务列表。窄屏下不横向滚动。
test("G2:新建和修改任务的页面,取消、后退回到任务列表", async ({ page, request }) => {
  await createTask(request, n("route"), "echo route");
  await openApp(page);
  const list = page.getByRole("heading", { name: "任务", exact: true });

  // 新建:地址是 #/new,浏览器后退回到列表。
  await page.getByRole("button", { name: "新建任务" }).click();
  await expect(taskForm(page)).toBeVisible();
  await expect(page).toHaveURL(/#\/new$/);
  await expect(list).toBeHidden();
  await page.goBack();
  await expect(list).toBeVisible();
  await expect(taskForm(page)).toBeHidden();

  // 浏览器前进又回到表单页,取消回到列表。
  await page.goForward();
  await expect(taskForm(page)).toBeVisible();
  await taskForm(page).getByRole("button", { name: "取消" }).click();
  await expect(list).toBeVisible();
  expect(new URL(page.url()).hash).toBe("");

  // 修改:地址带任务名,后退回到列表。
  await row(page, n("route")).getByRole("button", { name: "编辑" }).click();
  await expect(page).toHaveURL(new RegExp(`#/edit/${n("route")}$`));
  await expect(commandBox(taskForm(page))).toHaveValue("echo route");
  await page.goBack();
  await expect(list).toBeVisible();

  // 直接打开修改页的地址也能用,取消后回到列表。
  await page.goto(`${base()}/#/edit/${n("route")}`);
  await expect(commandBox(taskForm(page))).toHaveValue("echo route");
  await taskForm(page).getByRole("button", { name: "取消" }).click();
  await expect(list).toBeVisible();

  // 不存在的任务给出说明和返回的按钮。
  await page.goto(`${base()}/#/edit/${n("nope")}`);
  await expect(page.getByText(`没有名为 ${n("nope")} 的任务`)).toBeVisible();
  await page.getByRole("button", { name: "返回任务列表" }).click();
  await expect(list).toBeVisible();

  // 窄屏:表单页不出现横向滚动。
  await page.setViewportSize({ width: 375, height: 700 });
  await page.getByRole("button", { name: "新建任务" }).click();
  await taskForm(page).getByRole("radio", { name: "多个步骤" }).check();
  await expect(taskForm(page).getByRole("button", { name: "保存" })).toBeInViewport();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

// 删除放在修改任务的页面里,不和"编辑"挨在一起。
test("删除任务要先确认,确认后从列表消失", async ({ page, request }) => {
  await createTask(request, n("delete"), "true");
  await openApp(page);
  const r = row(page, n("delete"));
  await r.getByRole("button", { name: "编辑" }).click();
  const form = taskForm(page);

  page.once("dialog", (d) => d.dismiss());
  await form.getByRole("button", { name: "删除任务" }).click();
  await expect(form).toBeVisible();

  page.once("dialog", (d) => d.accept());
  await form.getByRole("button", { name: "删除任务" }).click();
  await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();
  await expect(r).toHaveCount(0);
  expect((await request.get(`${base()}/api/v1/tasks/${n("delete")}`, { headers: auth() })).status()).toBe(404);
});

// G4:运行历史可按任务筛选,点一行打开详情。
test("G4:运行记录按任务筛选,点一行打开详情", async ({ page, request }) => {
  await createTask(request, n("runs-a"), "echo a");
  await createTask(request, n("runs-b"), "echo b");
  await runTask(request, n("runs-a"));
  await runTask(request, n("runs-b"));

  await openApp(page);
  await page.getByRole("button", { name: "运行记录" }).click();
  await expect(page.getByRole("heading", { name: "运行记录" })).toBeVisible();
  await expect(row(page, n("runs-a"))).toHaveCount(1);
  await expect(row(page, n("runs-b"))).toHaveCount(1);

  await page.getByLabel("按任务").selectOption(n("runs-a"));
  await expect(row(page, n("runs-b"))).toHaveCount(0);
  await expect(row(page, n("runs-a"))).toHaveCount(1);

  await row(page, n("runs-a")).click();
  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${n("runs-a")} · 成功` })).toBeVisible();
  await expect(details.locator("pre").first()).toHaveText("a\n");
});

// 决策记录 0007:点系统通知时,桌面应用用 #run= 打开那次运行。
test("网址片段里的 run 直接打开那次运行的详情", async ({ page, request }) => {
  await createTask(request, n("handoff"), "echo handed-off");
  const id = await runTask(request, n("handoff"));

  await page.goto(`${base()}/#token=${token()}&run=${id}`);
  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${n("handoff")} · 成功` })).toBeVisible();
  await expect(details.locator("pre").first()).toHaveText("handed-off\n");
  // 令牌和运行编号都从地址栏里拿掉了。
  expect(new URL(page.url()).hash).toBe("");

  // 地址换成新建任务的页面时,详情关掉,表单能用。
  await page.evaluate(() => (location.hash = "#/new"));
  await expect(page.getByRole("heading", { name: "新建任务" })).toBeVisible();
  await expect(details).toBeHidden();
});

// G6:运行还没结束时,详情里就能看到已有的输出,随后续输出增长;结束后显示完整的结果。
test("G6:运行中的详情里看得到已经产生的输出", async ({ page, request }) => {
  const gate = join(mkdtempSync(join(tmpdir(), "bugu-e2e-")), "gate");
  const name = n("live");
  await createTask(request, name, `echo 第一行; while [ ! -f '${gate}' ]; do sleep 0.1; done; echo 第二行`);
  const resp = await request.post(`${base()}/api/v1/tasks/${name}/run`, { headers: auth() });
  const { id } = (await resp.json()) as { id: string };

  await page.goto(`${base()}/#token=${token()}&run=${id}`);
  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${name} · 运行中` })).toBeVisible();
  await expect(details.getByText("运行中,每秒更新")).toBeVisible();
  await expect(details.locator("pre").first()).toHaveText("第一行\n");

  writeFileSync(gate, "");
  await expect(details.getByRole("heading", { name: `${name} · 成功` })).toBeVisible();
  await expect(details.locator("pre").first()).toHaveText("第一行\n第二行\n");
  await expect(details.getByText("运行中,每秒更新")).toHaveCount(0);
});

// T5 T6 T8:多步骤运行的详情里逐步显示状态和输出;修改其他设置时步骤保持不变。
test("多步骤任务:详情里逐步显示结果,修改设置时步骤不变", async ({ page, request }) => {
  const name = n("steps");
  const resp = await request.post(base() + "/api/v1/tasks", {
    headers: auth(),
    data: {
      name,
      trigger: { cron: "0 9 * * *" },
      steps: [
        { name: "取数", type: "script", script: { command: "echo 三条" } },
        { type: "text", text: "共 {steps.取数.output}" },
        { name: "失败", type: "script", script: { command: "echo 坏了 >&2; exit 2" } },
        { type: "script", script: { command: "echo 不该运行" } },
      ],
    },
  });
  expect(resp.ok(), await resp.text()).toBe(true);

  await openApp(page);
  await row(page, name).getByRole("button", { name: "立即运行" }).click();
  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${name} · 失败` })).toBeVisible({ timeout: 15_000 });
  // T9:详情顶部每一步一张卡片,显示状态。
  const cards = details.getByRole("list", { name: "步骤" }).getByRole("listitem");
  await expect(cards).toHaveCount(4);
  await expect(cards.nth(0)).toContainText("成功");
  await expect(cards.nth(2)).toContainText("坏了");
  await expect(cards.nth(3)).toContainText("已跳过");
  const steps = details.locator("section.step");
  await expect(steps).toHaveCount(4);
  await expect(steps.nth(0)).toContainText("第 1 步 · 取数 · 脚本");
  await expect(steps.nth(0).locator("pre").first()).toHaveText("三条\n");
  await expect(steps.nth(1).locator("pre").first()).toHaveText("共 三条");
  await expect(steps.nth(2)).toContainText("失败");
  await expect(steps.nth(2).locator("pre.stderr")).toHaveText("坏了\n");
  await expect(steps.nth(3)).toContainText("已跳过");
  await expect(steps.nth(3).locator("pre")).toHaveCount(0);
  await details.getByRole("button", { name: "关闭" }).click();

  // 打开运行详情时页面切到了运行记录,先回到任务列表。
  await page.getByRole("button", { name: "任务", exact: true }).click();
  await row(page, name).getByRole("button", { name: "编辑" }).click();
  const form = taskForm(page);
  await expect(form.locator(".step-card")).toHaveCount(4);
  await form.getByLabel("静默标记(可选)").fill("NOTHING_NEW");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();
  const saved = await getTask(request, name);
  expect(saved.steps).toHaveLength(4);
  expect(saved.silent_if).toBe("NOTHING_NEW");
});

// G3:收件箱列出未读结果,按 Markdown 显示;打开即标为已读,也可以全部标为已读。
test("G3:收件箱按 Markdown 显示结果,读过之后不再算未读", async ({ page, request }) => {
  const report = n("inbox-report");
  const other = n("inbox-other");
  await createTask(request, report, "printf '# 今日新闻\\n\\n- 第一条\\n- **第二条**\\n'");
  await createTask(request, other, "echo 另一条结果");
  await runTask(request, report);
  await runTask(request, other);

  await openApp(page);
  await expect(page.getByRole("button", { name: /^收件箱/ }).getByLabel(/条未读/)).toBeVisible();
  await page.getByRole("button", { name: /^收件箱/ }).click();
  await expect(page.getByRole("heading", { name: "收件箱" })).toBeVisible();
  const r = row(page, report);
  await expect(r.getByLabel("未读")).toBeVisible();
  await expect(r).toContainText("今日新闻 第一条"); // 摘要是去掉 Markdown 标记的纯文字

  await r.click();
  const details = page.getByRole("dialog");
  const result = details.locator("section.result");
  await expect(result.getByRole("heading", { name: "今日新闻" })).toBeVisible();
  await expect(result.getByRole("listitem")).toHaveText(["第一条", "第二条"]);
  await expect(result.locator("strong")).toHaveText("第二条");
  await expect(details.locator("details.raw")).not.toHaveAttribute("open");
  await details.getByRole("button", { name: "关闭" }).click();

  // 还在收件箱里;读过的那条从"未读"里消失。
  await expect(page.getByRole("heading", { name: "收件箱" })).toBeVisible();
  await expect(row(page, report)).toHaveCount(0);
  await expect(row(page, other)).toHaveCount(1);
  await page.getByRole("radio", { name: "全部" }).check();
  await expect(row(page, report)).toHaveCount(1);
  await expect(row(page, report).getByLabel("未读")).toHaveCount(0);

  await page.getByRole("button", { name: "全部标为已读" }).click();
  await page.getByRole("radio", { name: "未读" }).check();
  await expect(page.getByText("没有未读的结果")).toBeVisible();
  await expect(page.getByRole("button", { name: /^收件箱/ }).getByLabel(/条未读/)).toHaveCount(0);
});

// E7 S4 N3 N7:表单里设置时区、补跑策略和 webhook;运行详情里看到 webhook 已送达。
// S9:定时用选择而不是 cron:选"每周"、勾星期、填时间;会写 cron 的人改成自定义,在等价的表达式上改。
test("S9:用选择设定时间,也可以改成自定义的 cron", async ({ page, request }) => {
  const name = n("pick");
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await form.getByLabel("名称").fill(name);
  await commandBox(form).fill("true");
  await form.getByRole("combobox", { name: "重复", exact: true }).selectOption("weekly");
  const days = form.getByRole("group", { name: "星期" });
  await days.getByLabel("周一").uncheck();
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form.getByRole("alert")).toContainText("每周至少选一天");
  await days.getByLabel("周一").check();
  await days.getByLabel("周五").check();
  await form.getByLabel("时间", { exact: true }).fill("08:30");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();
  expect((await getTask(request, name)).trigger).toEqual({ cron: "30 8 * * 1,5" });
  await expect(row(page, name)).toContainText("每周一、周五 08:30");

  await row(page, name).getByRole("button", { name: "编辑" }).click();
  await expect(form.getByRole("combobox", { name: "重复", exact: true })).toHaveValue("weekly");
  await form.getByRole("combobox", { name: "重复", exact: true }).selectOption("cron");
  const cron = form.getByRole("textbox", { name: "cron 表达式" });
  await expect(cron).toHaveValue("30 8 * * 1,5");
  await cron.fill("0 8,20 * * *");
  await expect(form).toContainText("即:每天 08:00、20:00");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();
  expect((await getTask(request, name)).trigger).toEqual({ cron: "0 8,20 * * *" });
  await row(page, name).getByRole("button", { name: "编辑" }).click();
  await expect(form.getByRole("combobox", { name: "重复", exact: true })).toHaveValue("cron");
});

// S12:表单里设随机偏移,列表里写在时间后面;太大时保存失败并说明原因。
test("S12:随机偏移在表单里设置,列表显示 ±分钟", async ({ page, request }) => {
  const name = n("jitter");
  await createTask(request, name, "true");
  await openApp(page);
  await row(page, name).getByRole("button", { name: "编辑" }).click();
  const form = taskForm(page);
  await form.getByLabel("随机偏移,分钟(可选)").fill("800");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form.getByRole("alert")).toContainText("随机偏移 13h20m0s 太大");
  await form.getByLabel("随机偏移,分钟(可选)").fill("10");
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();
  expect((await getTask(request, name)).trigger).toEqual({ cron: "0 9 * * *", jitter: "10m0s" });
  await expect(row(page, name)).toContainText("每天 09:00 ±10 分钟");
});

test("时区、补跑和 webhook:表单保存,运行详情显示投递情况", async ({ page, request }) => {
  const received: string[] = [];
  const hook = createServer((req, res) => {
    received.push(req.headers["idempotency-key"] as string);
    res.end("ok");
  });
  await new Promise<void>((done) => hook.listen(0, "127.0.0.1", done));
  const hookURL = `http://127.0.0.1:${(hook.address() as AddressInfo).port}/hook`;
  try {
    const name = n("hook");
    await createTask(request, name, "echo 已完成");
    await openApp(page);
    await row(page, name).getByRole("button", { name: "编辑" }).click();
    const form = taskForm(page);
    await form.getByLabel("时区(可选)").fill("Asia/Shanghai");
    await form.getByLabel("错过的触发").selectOption("skip");
    await form.getByLabel("Webhook 地址(可选)").fill(hookURL);
    await form.getByLabel("Webhook 什么时候发").selectOption("always");
    await form.getByRole("button", { name: "保存" }).click();
    await expect(form).toBeHidden();
    const saved = await getTask(request, name);
    expect(saved).toMatchObject({ timezone: "Asia/Shanghai", on_missed: "skip" });
    expect(saved.notifications).toContainEqual({ channel: "webhook", url: hookURL, when: "always" });

    await row(page, name).getByRole("button", { name: "立即运行" }).click();
    const details = page.getByRole("dialog");
    await expect(details.getByRole("heading", { name: `${name} · 成功` })).toBeVisible({ timeout: 15_000 });
    await expect(details.getByText(`Webhook ${hookURL}:已送达`)).toBeVisible();
    expect(received).toHaveLength(1);
    expect(received[0]).toMatch(/\/0$|\/1$/);
  } finally {
    hook.close();
  }
});

// G15 决策记录 0014:界面语言在设置页里切换,存进后台的设置;后台的错误信息也跟着说英文。
// 设置为跟随系统时用浏览器的语言。测试结束时改回跟随系统,不影响其他测试。
test("界面语言:切换到英文,后台的错误信息也是英文,改回跟随系统", async ({ page, request }) => {
  await openApp(page);
  await page.getByRole("button", { name: "设置", exact: true }).click();
  try {
    await page.getByRole("group", { name: "语言" }).getByRole("radio", { name: "English" }).check();
    await expect(page.getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Tasks", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Tasks", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "New task" })).toBeVisible();
    const settings = await (await request.get(base() + "/api/v1/settings", { headers: auth() })).json();
    expect(settings).toMatchObject({ language: "en", effective_language: "en" });

    // 后台给出的错误说明同样是英文。
    await page.getByRole("button", { name: "New task" }).click();
    const form = taskForm(page);
    await form.getByRole("textbox", { name: /^Name/ }).fill(n("lang"));
    await form.getByRole("combobox", { name: "Repeat", exact: true }).selectOption("cron");
    await form.getByRole("textbox", { name: /^Cron expression/ }).fill("not a cron");
    await form.getByRole("textbox", { name: /^Command/ }).fill("true");
    await form.getByRole("button", { name: "Save" }).click();
    await expect(form.getByRole("alert")).toContainText("cron expression");
    page.once("dialog", (d) => d.accept());
    await form.getByRole("button", { name: "Cancel" }).click();

    // 重新打开页面仍是英文。
    await openPage(page);
    await expect(page.getByRole("heading", { name: "Tasks", exact: true })).toBeVisible();
  } finally {
    await page.getByRole("button", { name: /^(设置|Settings)$/ }).click();
    await page.getByRole("group", { name: /^(语言|Language)$/ }).getByRole("radio", { name: /^(跟随系统|System)$/ }).check();
  }
  await page.getByRole("button", { name: "任务", exact: true }).click();
  await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();
});

async function openPage(page: Page) {
  await page.goto(`${base()}/#token=${token()}`);
}

// G16:外观在设置页里选择,只记在这个浏览器里;重新打开后仍然生效,改回跟随系统后按系统取色。
test("外观:选深色后页面变深,重新打开仍是深色,改回跟随系统", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await openApp(page);
  const background = () => page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  const light = await background();
  await page.getByRole("button", { name: "设置", exact: true }).click();
  await expect(page.getByText("版本")).toBeVisible();
  const appearance = page.getByRole("group", { name: "外观" });
  await appearance.getByRole("radio", { name: "深色" }).check();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  expect(await background()).not.toBe(light);

  await openPage(page);
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

  await page.getByRole("button", { name: "设置", exact: true }).click();
  await page.getByRole("group", { name: "外观" }).getByRole("radio", { name: "跟随系统" }).check();
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", /.*/);
  expect(await background()).toBe(light);
});

// T9:在步骤卡片上新建多步骤任务:单步内容成为第一步,添加、排序、删除后保存。
test("T9:用步骤卡片编辑多步骤任务", async ({ page, request }) => {
  const name = n("cards");
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await form.getByRole("textbox", { name: /^名称/ }).fill(name);
  await commandBox(form).fill("echo 第一");
  await form.getByRole("radio", { name: "多个步骤" }).check();
  const cards = form.locator(".step-card");
  await expect(cards).toHaveCount(1);
  await expect(cards.nth(0).getByRole("textbox", { name: /^命令/ })).toHaveValue("echo 第一");

  await form.getByRole("button", { name: "添加步骤" }).click();
  await cards.nth(1).getByLabel("类型").selectOption("text");
  await cards.nth(1).getByRole("textbox", { name: /^文本/ }).fill("要求");
  await form.getByRole("button", { name: "添加步骤" }).click();
  await cards.nth(2).getByRole("textbox", { name: /^命令/ }).fill("cat");
  // 把文本步骤移到最前,再删掉最后一步。
  await form.getByRole("button", { name: "前移第 2 步" }).click();
  await form.getByRole("button", { name: "删除第 3 步" }).click();
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form).toBeHidden();
  const saved = await getTask(request, name);
  expect(saved.steps).toEqual([
    { type: "text", text: "要求" },
    { type: "script", script: { command: "echo 第一" } },
  ]);
});

// 决策记录 0017:页面重新渲染时原地更新元素。逐字输入时焦点和内容不丢(G2);
// 步骤卡片调整顺序时,焦点跟着被移动的那一步(T9)。
test("重新渲染不打断输入,调整步骤顺序后焦点还在原来的按钮上", async ({ page }) => {
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  const name = form.getByRole("textbox", { name: /^名称/ });
  await name.pressSequentially("typed-in-place");
  await expect(name).toBeFocused();
  await expect(name).toHaveValue("typed-in-place");
  // 在中间插入:光标位置不因重新渲染跳到末尾。
  await name.press("Home");
  await name.pressSequentially("ab");
  await expect(name).toHaveValue("abtyped-in-place");

  await form.getByRole("radio", { name: "多个步骤" }).check();
  await form.getByRole("button", { name: "添加步骤" }).click();
  await form.getByRole("button", { name: "添加步骤" }).click();
  const cards = form.locator(".step-card");
  await expect(cards).toHaveCount(3);
  await cards.nth(2).getByRole("textbox", { name: /^命令/ }).fill("echo 第三");
  await form.getByRole("button", { name: "前移第 3 步" }).press("Enter");
  await expect(cards.nth(1).getByRole("textbox", { name: /^命令/ })).toHaveValue("echo 第三");
  await expect(form.getByRole("button", { name: "前移第 2 步" })).toBeFocused();
  page.once("dialog", (d) => d.accept());
  await form.getByRole("button", { name: "取消" }).click();
});

// G2:保存不成时,说明和保存按钮在一起,不用滚到表单末尾去找;有修改时离开要先确认。
test("G2:保存失败的说明就在保存按钮旁,离开有修改的表单要确认", async ({ page }) => {
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await form.getByLabel("名称").fill(n("unsaved"));
  await form.getByRole("button", { name: "保存" }).click();
  await expect(form.getByRole("alert")).toHaveText("请填写要运行的命令。");
  await expect(form.getByRole("alert")).toBeInViewport();

  // 不放弃:留在表单上,填的内容还在。换页同样要确认。
  page.once("dialog", (d) => d.dismiss());
  await form.getByRole("button", { name: "取消" }).click();
  await expect(form.getByLabel("名称")).toHaveValue(n("unsaved"));
  page.once("dialog", (d) => d.dismiss());
  await page.getByRole("button", { name: "运行记录" }).click();
  await expect(form).toBeVisible();
  page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "运行记录" }).click();
  await expect(page.getByRole("heading", { name: "运行记录" })).toBeVisible();
});

// G1 G4:任务列表里看得到上次的结果,失败的不用去翻运行记录;正在运行的任务不能再点立即运行。
test("G1:任务列表显示上次结果,运行中的任务不能再次运行", async ({ page, request }) => {
  const [bad, slow] = [n("last-bad"), n("last-slow")];
  await createTask(request, bad, "echo 连不上 >&2; exit 3");
  await createTask(request, slow, "sleep 30");
  const resp = await request.post(`${base()}/api/v1/tasks/${bad}/run`, { headers: auth() });
  const { id } = (await resp.json()) as { id: string };
  await expect.poll(async () => (await (await request.get(`${base()}/api/v1/runs/${id}`, { headers: auth() })).json()).status).toBe("failed");
  await request.post(`${base()}/api/v1/tasks/${slow}/run`, { headers: auth() });

  await openApp(page);
  await expect(row(page, bad)).toContainText("失败");
  await expect(row(page, slow)).toContainText("运行中");
  await expect(row(page, slow).getByRole("button", { name: "立即运行" })).toBeDisabled();

  // 点上次结果打开那次运行,留在任务列表这一页;失败的详情先显示标准错误。
  await row(page, bad).getByRole("button", { name: /失败/ }).click();
  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${bad} · 失败` })).toBeVisible();
  await expect(details.locator("pre").first()).toHaveText("连不上\n");
  await details.getByRole("button", { name: "关闭" }).click();
  await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();

  // 运行记录里,单步任务失败的说明是输出的开头。
  await page.getByRole("button", { name: "运行记录" }).click();
  await expect(row(page, bad)).toContainText("连不上");
});

// 窄屏和内容很长时页面不横向滚动,每一处都还点得到(G2、G4、T9)。
test("窄屏、长任务名、多张步骤卡片都不把页面撑宽", async ({ page, request }) => {
  const long = (n("wide") + "-" + "x".repeat(64)).slice(0, 64);
  await createTask(request, long, "echo wide");
  await runTask(request, long);
  const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);

  // 宽屏:名字截断,操作按钮还在屏幕里。
  await page.setViewportSize({ width: 1024, height: 768 });
  await openApp(page);
  await expect(row(page, long)).toBeVisible();
  expect(await page.locator(".scroll").evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  expect(await fits()).toBe(true);

  // 四张步骤卡片:卡片那一排自己滚动,新加的一张滚进视野。
  await page.getByRole("button", { name: "新建任务" }).click();
  const form = taskForm(page);
  await form.getByRole("radio", { name: "多个步骤" }).check();
  for (let i = 0; i < 3; i++) await form.getByRole("button", { name: "添加步骤" }).click();
  await expect(form.locator(".step-card")).toHaveCount(4);
  await expect(form.locator(".step-card").nth(3)).toBeInViewport();
  expect(await fits()).toBe(true);
  page.once("dialog", (d) => d.accept());
  await form.getByRole("button", { name: "取消" }).click();

  // 手机宽度:每个标签页都点得到,列表不横向滚动。
  await page.setViewportSize({ width: 375, height: 700 });
  for (const tab of ["任务", "收件箱", "运行记录", "设置"]) {
    await page.getByRole("button", { name: tab, exact: tab !== "收件箱" }).click();
    await expect(page.getByRole("heading", { name: tab, exact: true })).toBeVisible();
    expect(await fits(), tab).toBe(true);
  }
  await page.getByRole("button", { name: "运行记录" }).click();
  await expect(row(page, long).first()).toContainText("成功");
  await expect(row(page, long).first().getByText("成功")).toBeInViewport();
});

// G5 G14:设置页里登记另一个布谷,显示它的连接状态;连不上的显示为离线。
test("其他主机:登记、显示状态、删除", async ({ page, request }) => {
  const resp = await request.post(base() + "/api/v1/tokens", { headers: auth(), data: { name: n("peer") } });
  expect(resp.ok(), await resp.text()).toBe(true);
  const peer = (await resp.json()) as { token: string };
  await openApp(page);
  await page.getByRole("button", { name: "设置", exact: true }).click();
  const section = page.getByRole("group", { name: "其他主机" });
  for (const [name, url, token] of [
    [n("self"), base(), peer.token],
    [n("gone"), "http://127.0.0.1:9", "x"],
  ]) {
    await section.getByRole("textbox", { name: /^名称/ }).fill(name);
    await section.getByRole("textbox", { name: /^地址/ }).fill(url);
    await section.getByLabel(/^令牌/).fill(token);
    await section.getByRole("button", { name: "登记" }).click();
    await expect(section.getByRole("listitem").filter({ hasText: name })).toBeVisible();
  }
  await expect(section.getByRole("listitem").filter({ hasText: n("self") })).toContainText("在线");
  await expect(section.getByRole("listitem").filter({ hasText: n("gone") })).toContainText("离线");
  // 连不上的主机不能打开,免得窗口停在错误页。
  await expect(section.getByRole("listitem").filter({ hasText: n("gone") }).getByRole("button", { name: "打开" })).toBeDisabled();
  await expect(section.getByRole("listitem").filter({ hasText: n("self") }).getByRole("button", { name: "打开" })).toBeEnabled();
  page.once("dialog", (d) => d.accept());
  await section.getByRole("listitem").filter({ hasText: n("gone") }).getByRole("button", { name: "删除" }).click();
  await expect(section.getByRole("listitem").filter({ hasText: n("gone") })).toHaveCount(0);
  page.once("dialog", (d) => d.accept());
  await section.getByRole("listitem").filter({ hasText: n("self") }).getByRole("button", { name: "删除" }).click();
  await request.delete(`${base()}/api/v1/tokens/${n("peer")}`, { headers: auth() });
});

// G5:从本机打开另一台主机时,整条标识写明正在管理哪台,标题以它的名字开头,
// 删除确认里也写明主机;本机的面板没有这条标识。
test("另一台主机的面板标明是哪台主机", async ({ page, request }) => {
  const name = n("remote");
  await createTask(request, name, "true");
  await openApp(page);
  await expect(page.getByRole("status").filter({ hasText: "正在管理另一台主机" })).toHaveCount(0);
  await expect(page).toHaveTitle("布谷");

  // 本机打开它时带上的网址片段:令牌、本机面板的地址、在本机登记的名字。
  await page.goto(`${base()}/#token=${token()}&home=${encodeURIComponent("http://localhost:9")}&name=nas`);
  const bar = page.getByRole("status").filter({ hasText: "正在管理另一台主机" });
  await expect(bar).toContainText("nas");
  await expect(bar.getByRole("link", { name: "返回本机" })).toHaveAttribute("href", "http://localhost:9/");
  await expect(page).toHaveTitle("nas · 布谷");
  await row(page, name).getByRole("button", { name: "编辑" }).click();
  page.once("dialog", (d) => {
    expect(d.message()).toContain(`删除 nas 上的任务 ${name}`);
    d.dismiss();
  });
  await taskForm(page).getByRole("button", { name: "删除任务" }).click();
  await taskForm(page).getByRole("button", { name: "取消" }).click();
  await expect(row(page, name)).toHaveCount(1);
});

// G5:顶栏的机器下拉框。本机页面选一台就打开它;在另一台主机的页面里选"本机"回来,
// 选别的主机时先回到本机、由本机打开(令牌只在本机)。同一个后台的 localhost 地址当作"另一台主机"。
test("机器下拉框:在本机和其他主机之间切换", async ({ page, request }) => {
  const tok = await request.post(base() + "/api/v1/tokens", { headers: auth(), data: { name: n("switch") } });
  const peer = ((await tok.json()) as { token: string }).token;
  const other = base().replace("127.0.0.1", "localhost");
  const [a, b] = [n("hostA"), n("hostB")];
  for (const name of [a, b]) {
    const resp = await request.put(`${base()}/api/v1/hosts/${name}`, { headers: auth(), data: { url: other, token: peer } });
    expect(resp.ok(), await resp.text()).toBe(true);
  }
  try {
    await openApp(page);
    const machine = page.getByRole("combobox", { name: "当前机器" });
    await expect(machine).toHaveValue("__local");

    await machine.selectOption(a);
    const bar = page.getByRole("status").filter({ hasText: "正在管理另一台主机" });
    await expect(bar).toContainText(a);
    await expect(machine).toHaveValue(a);

    // 在另一台主机的页面里选别的主机:经本机转一次,打开的是 b。
    await machine.selectOption(b);
    await expect(bar).toContainText(b);
    await expect(page).toHaveTitle(`${b} · 布谷`);

    // 选"本机"回来,不用重新输入令牌。
    await machine.selectOption("__local");
    await expect(page.getByRole("heading", { name: "任务", exact: true })).toBeVisible();
    await expect(bar).toHaveCount(0);
    await expect(page).toHaveURL(base() + "/");
  } finally {
    for (const name of [a, b]) await request.delete(`${base()}/api/v1/hosts/${name}`, { headers: auth() });
    await request.delete(`${base()}/api/v1/tokens/${n("switch")}`, { headers: auth() });
  }
});
