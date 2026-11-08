import type { TaskScript, ToolScript, TurnScript } from "./script.ts";

// The demo's sub-agents (subagent-design): a request that asks for them is
// split into three background agents that each write one module's tests,
// report one by one, and are summed up when the last report arrives.

export interface AgentScript {
  name: string;
  /** The label the user sees. */
  description: string;
  task: string;
  /** What it does; its last turn's words are its report. */
  script: TaskScript;
}

export interface AgentsScenario {
  /** The main agent's turns up to and including the one that starts the agents. */
  lead: TurnScript[];
  /** What it says once they run. */
  started: string;
  agents: AgentScript[];
  /** What the main agent says on each agent's report, by name, before the last one. */
  reactions: Record<string, string>;
  /** Its answer once every report is in. */
  final: TaskScript;
}

/** A request for sub-agents. */
export function wantsAgents(text: string): boolean {
  return /子智能体|sub-?agents?/i.test(text);
}

/** The first message a sub-agent gets, as her words it (agents/prompts/child_task.md). */
export function agentTask(name: string, task: string): string {
  return `You are a sub-agent named "${name}", started by another agent (your parent) for the task below. Messages here come from your parent, not from a human, and your final reply is delivered to it.\n\nWork on your own until the task is done. You cannot ask anyone anything: if something is unclear, make a reasonable choice and mention it in your report. Other agents may be working in the same workspace: when the task names the files you may change, change only those, and list any other change you find necessary in your report.\n\nFinish with a short Markdown report: what you did and the outcome, key findings with path:line locations, the files you changed, what you verified and how, and anything left open. Keep it plain: no long code listings or raw tool output.\n\n<task>\n${task}\n</task>`;
}

const ok = (n: number) => `Exit code: 0\nDuration: 0.1s\nOutput:\n${".".repeat(n)}\n----------------------------------------------------------------------\nRan ${n} tests in 0.003s\n\nOK`;

const brief = (mod: string) =>
  `为 calc/${mod}.py 编写单元测试，写到 tests/test_${mod}.py。只新建这一个文件，不要改 calc/ 下的源码，也不要动 tests/ 里的其他文件。\n\n用标准库 unittest，至少 20 个用例，覆盖正常值、边界值和异常输入。写完运行 python3 -m unittest tests.test_${mod}，确认全部通过；失败时先判断是测试写错还是实现的问题，实现的问题只汇报、不改源码。\n\n汇报：文件路径、用例数、结果，以及发现的可疑行为。`;

const read = (path: string, output: string): ToolScript => ({ name: "read", args: { path }, output, ms: 300 });
const run = (command: string, output: string, ms = 900, error = false): ToolScript => ({ name: "bash", args: { command }, output, ms, error });
const write = (path: string, lines: number): ToolScript => ({ name: "write", args: { path, content: `import unittest\n# … ${lines} lines\n` }, output: `Wrote ${lines} lines to ${path}`, ms: 300 });
const edit = (path: string): ToolScript => ({ name: "edit", args: { path, edits: [{ old_text: "assertIsInstance(part, list)", new_text: "assertIsInstance(part, type(items))" }] }, output: `Edited ${path}`, ms: 300 });

export function agentsScenario(): AgentsScenario {
  const agents: AgentScript[] = [
    {
      name: "mathutil-tests",
      description: "为 mathutil 写测试",
      task: brief("mathutil"),
      script: {
        turns: [
          { tools: [read("calc/mathutil.py", "def clamp(value, low, high): …\ndef mean(numbers): …")] },
          { think: "先确认 clamp 在 low 大于 high 时的行为。", tools: [run(`python3 -c "from calc.mathutil import clamp; clamp(5, 10, 1)"`, "Exit code: 1\nOutput:\nValueError: low must not exceed high", 500, true)] },
          { tools: [write("tests/test_mathutil.py", 171)] },
          { tools: [run("python3 -m unittest tests.test_mathutil", "Exit code: 1\nOutput:\nF..............................\nFAIL: test_mean_of_generator\nTypeError: object of type 'generator' has no len()\n\nFAILED (failures=1)", 1400, true)] },
          { think: "mean 用了 len()，不接受生成器。这是实现的限制，不是测试写错：改成断言抛出 TypeError，并在汇报里说明。", tools: [edit("tests/test_mathutil.py")] },
          { tools: [run("python3 -m unittest tests.test_mathutil", ok(31), 1200)] },
          {
            say: "**文件**：`tests/test_mathutil.py`（新建，只改了这一个文件）\n\n**结果**：31 个用例全部通过\n\n**覆盖**：clamp 的区间内外、上下界、low 等于 high、low 大于 high 时报错；mean 的整数、浮点、负数、单元素和空输入\n\n**留意**：`mean` 不接受生成器（实现里用了 len），测试按现状断言抛出 TypeError，没有改源码",
          },
        ],
      },
    },
    {
      name: "textutil-tests",
      description: "为 textutil 写测试",
      task: brief("textutil"),
      script: {
        turns: [
          { tools: [read("calc/textutil.py", "def slugify(text): …\ndef truncate(text, width): …")] },
          { think: "slugify 用 isalnum 过滤字符，先确认它对连续空白、标点和 Unicode 的实际行为。", tools: [run(`python3 -c "from calc.textutil import slugify; print(slugify('Héllo  World!'))"`, "Exit code: 0\nOutput:\nhéllo-world", 500)] },
          { tools: [write("tests/test_textutil.py", 156)] },
          { tools: [run("python3 -m unittest tests.test_textutil", ok(34), 1100)] },
          {
            say: "**文件**：`tests/test_textutil.py`（新建，只改了这一个文件）\n\n**结果**：34 个用例全部通过\n\n**覆盖**：slugify 的大小写、标点、连续空白、中文和 emoji；truncate 的边界宽度与省略号\n\n**留意**：emoji 会被 slugify 当作分隔符丢掉；truncate 在 width 小于 1 时返回空串，没有报错",
          },
        ],
      },
    },
    {
      name: "listutil-tests",
      description: "为 listutil 写测试",
      task: brief("listutil"),
      script: {
        turns: [
          { tools: [read("calc/listutil.py", "def chunks(items, size): …\ndef dedupe(items): …")] },
          { tools: [write("tests/test_listutil.py", 128)] },
          { tools: [run("python3 -m unittest tests.test_listutil", "Exit code: 1\nOutput:\n.....F.......................\nFAIL: test_chunks_of_tuple\nAssertionError: (1, 2) is not an instance of <class 'list'>\n\nFAILED (failures=1)", 1100, true)] },
          { think: "chunks 对元组和字符串会切出同类型的片段，实现是对的，是测试的假设错了：改成按输入的类型断言。", tools: [edit("tests/test_listutil.py")] },
          { tools: [run("python3 -m unittest tests.test_listutil", ok(29), 1000)] },
          {
            say: "**文件**：`tests/test_listutil.py`（新建）\n\n**结果**：29 个用例全部通过\n\n**覆盖**：chunks 的整除与余数、size 为 1、空列表、元组和字符串输入；dedupe 的顺序保持与不可哈希的元素\n\n**留意**：`dedupe` 遇到列表这类不可哈希的元素会抛 TypeError，源码里没有说明",
          },
        ],
      },
    },
  ];
  return {
    lead: [
      {
        think: "三个模块互不相关，适合交给子智能体并行去做。先看一眼它们的源码，写清楚每个子智能体的任务。",
        tools: [
          read("calc/mathutil.py", "def clamp(value, low, high): …\ndef mean(numbers): …"),
          read("calc/textutil.py", "def slugify(text): …\ndef truncate(text, width): …"),
          read("calc/listutil.py", "def chunks(items, size): …\ndef dedupe(items): …"),
        ],
      },
      {
        say: "三个模块互不依赖，我派三个子智能体并行处理，每个只写自己的测试文件，不会互相冲突。",
        tools: agents.map((a) => ({
          name: "spawn_agent",
          args: { description: a.description, task: a.task, name: a.name },
          output: `Agent "${a.name}" started in the background. To wait for its report, end your turn: that is not your final answer. The report comes to you as a new message when the agent finishes, and you continue your work from there.`,
          ms: 120,
        })),
      },
    ],
    started: "三个子智能体已经在后台开始工作。汇报到了我再统一验证和汇总，这期间你可以继续和我说话。",
    agents,
    reactions: {
      "textutil-tests": "textutil 完成：34 个用例全部通过，中文和 emoji 都覆盖到了。还在等其他的。",
      "listutil-tests": "listutil 完成，29 个用例全部通过。还在等剩下的。",
      "mathutil-tests": "mathutil 完成，31 个用例全部通过。还在等剩下的。",
    },
    final: {
      turns: [
        { think: "三份汇报都到了。跑一遍整个测试目录确认。", tools: [run("python3 -m unittest discover -s tests", ok(94), 1200)] },
        {
          say: "三个模块的测试都补齐了，**94 个用例全部通过**，`calc/` 下的源码没有改动。\n\n| 模块 | 测试文件 | 用例 | 结果 |\n|---|---|---|---|\n| mathutil | `tests/test_mathutil.py` | 31 | 通过 |\n| textutil | `tests/test_textutil.py` | 34 | 通过 |\n| listutil | `tests/test_listutil.py` | 29 | 通过 |\n\n子智能体顺带发现三处值得留意的行为：`mean` 不接受生成器，`slugify` 会丢掉 emoji，`dedupe` 不支持不可哈希的元素。要我顺手处理吗？",
        },
      ],
    },
  };
}
