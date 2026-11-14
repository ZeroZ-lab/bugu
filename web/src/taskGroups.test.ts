import { describe, expect, it } from "vitest";
import { groupByDir } from "./taskGroups";

const task = (name: string, cwd?: string) => ({ name, cwd });
const shape = (tasks: { name: string; cwd?: string }[]) => groupByDir(tasks).map((g) => [g.label, g.tasks.map((t) => t.name)]);

// G1:任务列表按工作目录分组。
describe("groupByDir", () => {
  it("同一目录的任务归在一组,组按名字排,组内保持原来的顺序,没有目录的放最后", () => {
    expect(
      shape([
        task("morning", "/Users/me/workspace/bugu"),
        task("observe", "/Users/me/workspace/x"),
        task("loose"),
        task("review", "/Users/me/workspace/x/"),
        task("adhoc", "  "),
        task("build", "/Users/me/workspace/bugu"),
      ]),
    ).toEqual([
      ["bugu", ["morning", "build"]],
      ["x", ["observe", "review"]],
      ["", ["loose", "adhoc"]],
    ]);
  });

  it("最后一段重名时多带上级目录,直到分得开", () => {
    expect(shape([task("a", "/w/one/app"), task("b", "/w/two/app"), task("c", "/w/tool"), task("d", "~/app")])).toEqual([
      ["~/app", ["d"]],
      ["one/app", ["a"]],
      ["tool", ["c"]],
      ["two/app", ["b"]],
    ]);
  });

  it("根目录和相对目录也有组名", () => {
    expect(shape([task("a", "/"), task("b", "scripts")])).toEqual([
      ["/", ["a"]],
      ["scripts", ["b"]],
    ]);
  });
});
