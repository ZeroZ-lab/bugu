// 网页前端的主要流程,对真实后台运行。只用脚本任务,不调用任何 AI 命令行。
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
const commandBox = (dialog: Locator) => dialog.getByRole("textbox", { name: /^命令/ });

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
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "新建任务" })).toBeVisible();
  await dialog.getByLabel("名称").fill(n("hello"));
  await commandBox(dialog).fill("echo hello-from-e2e");
  await dialog.getByRole("button", { name: "保存" }).click();
  await expect(dialog).toBeHidden();

  const r = row(page, n("hello"));
  await expect(r).toContainText("等待");
  await r.getByRole("button", { name: "立即运行" }).click();

  const details = page.getByRole("dialog");
  await expect(details.getByRole("heading", { name: `${n("hello")} · 成功` })).toBeVisible({ timeout: 15_000 });
  await expect(details.locator("pre").first()).toHaveText("hello-from-e2e\n");
  await details.getByRole("button", { name: "关闭" }).click();
  await expect(details).toBeHidden();
});

// G2:表单不合规时不提交,说明留在对话框里。
test("G2:名称不合规时浏览器拦下表单,任务没有建出来", async ({ page, request }) => {
  await openApp(page);
  await page.getByRole("button", { name: "新建任务" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称").fill("有空格 的名字");
  await commandBox(dialog).fill("true");
  await dialog.getByRole("button", { name: "保存" }).click();
  await expect(dialog).toBeVisible();
  expect(await dialog.getByLabel("名称").evaluate((el: HTMLInputElement) => el.validity.patternMismatch)).toBe(true);
  await dialog.getByRole("button", { name: "取消" }).click();
  const tasks = (await (await request.get(base() + "/api/v1/tasks", { headers: auth() })).json()) as unknown[];
  expect(tasks.some((t) => (t as { name: string }).name.includes(" "))).toBe(false);
});

// G1:暂停和恢复。
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

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "修改任务" })).toBeVisible();
  await expect(dialog.getByLabel("名称")).toHaveValue(n("edit"));
  await expect(dialog.getByLabel("名称")).toBeDisabled();
  const command = commandBox(dialog);
  await expect(command).toHaveValue("echo before");
  await command.fill("echo after");
  await dialog.getByRole("button", { name: "保存" }).click();
  await expect(dialog).toBeHidden();

  expect((await getTask(request, n("edit"))).steps[0].script.command).toBe("echo after");
});

test("删除任务要先确认,确认后从列表消失", async ({ page, request }) => {
  await createTask(request, n("delete"), "true");
  await openApp(page);
  const r = row(page, n("delete"));

  page.once("dialog", (d) => d.dismiss());
  await r.getByRole("button", { name: "删除" }).click();
  await expect(r).toBeVisible();

  page.once("dialog", (d) => d.accept());
  await r.getByRole("button", { name: "删除" }).click();
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

  await page.getByLabel("任务").selectOption(n("runs-a"));
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
});
