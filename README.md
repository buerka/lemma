# Lemma

> A minimal, git-native progress ledger for human–agent research projects, with remote execution through [ssh-mcp](https://github.com/buerka/ssh-mcp).

**Name:** Lemma · **Command:** `lem` (alias: `lemma`) · **Status:** v0.1 implemented · **Go:** 1.22+ · **License:** MIT

[![ci](https://github.com/buerka/lemma/actions/workflows/ci.yml/badge.svg)](https://github.com/buerka/lemma/actions/workflows/ci.yml)
[![release](https://github.com/buerka/lemma/actions/workflows/release.yml/badge.svg)](https://github.com/buerka/lemma/actions/workflows/release.yml)

项目名是 **Lemma**，默认命令是 **`lem`**；`lemma` 作为完整别名保留。

`lem` 让主 agent 只做流程管理，不碰具体实现：worker 写代码、跑实验，主 agent 只看结构化摘要；人类通过 `REVIEW.md`、哈希批准和 git commit 掌握每一次状态变更。

它不替你做训练，也不替你判断科学结论。它只回答四个问题：

1. 现在做到哪了？
2. 下一步该做什么？
3. 这个结果是用哪份代码、配置、数据跑出来的？
4. 谁在什么时候批准了什么？

`lem` 本身是一个普通 CLI，任何能执行 shell 的 agent 都能用。远端执行通过 [ssh-mcp](https://github.com/buerka/ssh-mcp) 完成；ZCode、Codex、Claude Code、Cursor 等 MCP 客户端都可以接。

---

## Why

研究项目里的 agent 工作流通常有三个问题：

- **上下文污染**：主 agent 读代码、日志、报错，几轮之后就不再“管理”，而是在“实现”。
- **状态丢失**：会话一断，新 agent 不知道上次做到哪、为什么这么定。
- **结果不可追溯**：一个数字出来了，但说不清它对应哪次 run、哪份数据、哪个 commit。

`lem` 的做法是把状态从对话里拿出来，放进一个很小的、git 跟踪的账本：

- `progress/` 只存 manifest、事件、变更单和摘要，不存大文件；
- 每次状态变更都经过 `plan → approve → apply`，人类批准的是一个哈希；
- 每个 run 记录 `code_commit + config_hash + data_hash + environment_hash`；
- 大文件（checkpoint、日志、预测）留在远端，按 URI + SHA-256 引用。

---

## Why Go

`lem` 是一个很小的状态机 + 文件 I/O 工具，不参与训练。用 Go 不是因为“训练需要 Go”，而是因为运维和分发更省事：

- **单静态二进制**：`CGO_ENABLED=0` 构建，无 Python、无 venv、无依赖冲突。
- **常驻内存低**：CLI 进程几 MB；相比 Python 脚本，启动和内存更轻。绝对值不大，但多 agent 反复调用时更干净。
- **启动快**：CLI 反复调用时，Go 的启动延迟明显低于 Python。
- **与 ssh-mcp 同栈**：同为 Go 单二进制、同一种发布方式、同一套跨平台构建。
- **类型安全**：schema 固定，Go struct 比动态字典更不容易静默出错。

训练本身仍然是 Python/PyTorch；`lem` 只负责编排、状态和哈希，不碰 GPU。

---

## What it is / is not

**It is**

- 一个文件化的进度账本（`progress/`）
- 一个变更管理协议（plan / approve / apply）
- 一个薄 CLI（`lem`）
- 一组可校验的 schema

**It is not**

- 不是调度器（不替代 Slurm / tmux）
- 不是实验追踪 UI（不替代 MLflow / W&B）
- 不是数据库（不引入 server / SQLite / Postgres）
- 不是 TUI（人类看 Markdown / 静态 HTML）
- 不是数据版本控制（不替代 DVC；大文件按哈希引用）

---

## Design principles

1. **Git is the commit layer.** 每个状态变更都对应一个 git commit；代码、配置、事件、变更单都在里面。
2. **Append-only.** `events.jsonl` 只追加，不修改；失败也留痕。
3. **Plan before apply.** 任何状态变更先生成 changeset 和 `REVIEW.md`，人类批准哈希后才执行。
4. **Control plane / data plane 分离.** `progress/` 存元数据；checkpoint、日志、数据集放远端或对象存储。
5. **Transport-agnostic.** `lem` 不实现 SSH；远端执行由 runner 子代理通过 [ssh-mcp](https://github.com/buerka/ssh-mcp) 完成。
6. **Structured handoffs.** worker 只返回固定 schema 的 JSON；主 agent 不接受自由文本。
7. **Human gates.** 划分、unknown 协议、全量训练、最终结论必须人工批准。
8. **Small surface.** 只保留少量核心命令；不做 dashboard、不做 TUI、不做插件系统。
9. **Per-project isolation + optimistic concurrency.** 每个项目独立 ledger、锁、revision；写操作带 `expected_revision`，冲突显式返回。

---

## Architecture

```text
                    ┌──────────────────────────────┐
                    │            Human             │
                    │  chat / REVIEW.md / git log  │
                    └──────────────┬───────────────┘
                                   │ approve / reject
                    ┌──────────────▼───────────────┐
                    │         Manager Agent        │
                    │  reads STATE.md, TASKS.yaml  │
                    │  never reads src/ or logs    │
                    └───────┬──────────────┬───────┘
                            │              │
                  lem CLI  │              │ spawn
                            │              │
                    ┌───────▼───────┐  ┌───▼─────────────────┐
                    │   progress/   │  │   Runner Subagent   │
                    │  events.jsonl │  │  uses ssh-mcp tools │
                    │  changesets/  │  │  put spec, poll,    │
                    │  runs/        │  │  fetch manifest     │
                    └───────┬───────┘  └───┬─────────────────┘
                            │              │
                            │ git commit   │ ssh_exec / ssh_put / ssh_get
                            │              │
                    ┌───────▼───────┐  ┌───▼─────────────────┐
                    │  Git History  │  │   SSH host(s)       │
                    │  audit trail  │  │  <workdir>          │
                    └───────────────┘  │  <run_root>/        │
                                       └─────────────────────┘
```

主 agent 永远不直接使用 ssh-mcp；runner 子代理的上下文在任务结束后丢弃。

多项目时，每个项目有独立的 `progress/`、`.lock` 和 git 仓库；`lem` 通过 registry 或 `--project` 解析 project，不共享可变状态。

---

## Multi-project and concurrent agents

`lem` 必须假设：同一个二进制会被多个 agent、多个会话、多个项目同时调用。设计上不做“全局当前项目”，而是**按项目寻址 + 每项目锁 + 乐观并发**。

### Project addressing

注册表 `~/.config/lem/projects.yaml`（`$LEM_CONFIG_DIR` 或
`$XDG_CONFIG_HOME/lem` 可覆盖，便于 CI/测试）：

```yaml
projects:
  road:
    root: /Users/me/research/RA
    ledger: progress
  other:
    root: /Users/me/research/other
    ledger: progress
default: road
```

CLI 解析顺序：

1. `--project <id|path>`
2. `LEM_PROJECT` 环境变量
3. 从 cwd 向上找 `progress/project.yaml`
4. 注册表 `default`

```bash
lem --project road status
LEM_PROJECT=other lem next --json
```

每个 agent 每次调用都显式带项目：

```bash
lem --project road status
lem --project other next --json
```

没有“当前项目”的可变状态；多 agent 各自起 CLI 进程，靠 `progress/.lock` 和 `revision` 协调。

### Concurrency control

每个项目独立：

- `progress/.lock`：写操作 `flock` 排他；读操作读原子快照。
- `revision`：最后一个 event 的哈希（或单调计数）。
- 读工具返回 `revision`。
- 写工具支持 `--expected-revision <hash>`；不匹配返回 `CONFLICT`，附带当前 revision 和变更摘要。
  这个选项覆盖 `task add/update`、`run start/finish`、`plan/review/approve/apply`。
  `apply` 还会强制 `changeset.base_revision` 等于当前 revision。
- `changeset_hash` 是幂等键；重复 `apply` 返回同一 receipt。

`lem apply` 的顺序：

```text
acquire lock
  -> re-read state
  -> verify changeset_hash + expected_revision + gate
  -> write temp files
  -> fsync + rename
  -> append event (prev_hash)
  -> git add progress/ && git commit   # state + event
  -> write receipt
  -> git commit                        # receipt
release lock
```

错误语义：

| Code | Meaning | Agent action |
|---|---|---|
| `BUSY` | 锁被占用 | 1–2s 后重试 |
| `CONFLICT` | revision 不匹配 | 重新 `lem status` / `lem plan`，不要盲目重试 |
| `IDEMPOTENT` | changeset 已应用 | 读取已有 receipt |
| `STALE` | schema 版本不匹配 | `lem migrate` |

### Git isolation

建议把 `progress/` 做成**独立 git 仓库**（`progress/.git`），避免和代码仓库的其他 agent 提交互相干扰。如果必须放在项目仓库里：

- `lem` 只 `git add progress/`，`git commit -- progress/`；
- 不要 `git add -A`；
- 遇到 `index.lock` 用指数退避重试。

### Cross-machine caveat

`flock` 在本地文件系统可靠；NFS/SMB 的锁语义不一致。跨机器写同一个 ledger 时：

- 限制同一项目只有一个写者，其他机器只读；
- 或者后续加一个轻量 HTTP 写服务（不是 v0.1 范围）。

当前 v0.1 假设：**同一台机器、普通 CLI 进程、每项目一个 ledger**。

### Testing

- 并发 apply：N 个 goroutine 同时提交，只有一个成功，其余 `CONFLICT`/`BUSY`；事件链完整。
- 多项目：两个项目并行跑，互不阻塞、互不串状态。
- 崩溃恢复：`kill -9` 在 apply 中间，锁释放、临时文件清理、`lem verify` 通过。

---

## Dependencies

### Required

- Go 1.22+（仅构建时需要；运行只需要二进制）
- git

### Remote execution

- [ssh-mcp](https://github.com/buerka/ssh-mcp) — Go 1.25+，单个静态二进制，stdio-only MCP server。
- 一个支持 MCP 的 agent（ZCode、Codex、Claude Code、Cursor 等）。
- 可选：远端 `tmux`，用于长任务；`python3`；`sha256sum`。

`lem` 不直接调用 `ssh`，也不依赖任何特定 agent。它只生成 `run_spec.json` 和 `REMOTE_TASK.md`；runner 子代理用 ssh-mcp 执行。

### Install ssh-mcp

```bash
git clone https://github.com/buerka/ssh-mcp.git
cd ssh-mcp
go build -o ssh-mcp .
```

把二进制放到 `PATH`，或写绝对路径到 agent 的 MCP 配置。

#### ZCode

```json
{
  "mcpServers": {
    "ssh": {
      "command": "/path/to/ssh-mcp"
    }
  }
}
```

#### Codex

`~/.codex/config.toml`：

```toml
[mcp_servers.ssh]
command = "/path/to/ssh-mcp"
```

#### Claude Code

```bash
claude mcp add ssh -- /path/to/ssh-mcp
```

#### Cursor

在 MCP 设置中加入同样的 `command: /path/to/ssh-mcp`。

不同客户端会给工具加前缀，例如 ZCode 暴露为 `mcp__ssh__ssh_exec`，其他客户端可能是 `ssh_exec`。runner 提示词应写 ssh-mcp 的工具名（`ssh_exec`、`ssh_put`、`ssh_get`、`ssh_ls`、`ssh_output`），让 agent 自行匹配前缀。

### SSH host

在 `~/.ssh/config` 里定义一个别名：

```sshconfig
Host gpu
  HostName 10.0.0.10
  User alice
  Port 22
  IdentityFile ~/.ssh/id_ed25519
```

`ssh-mcp` 通过 `ssh_hosts` 列出这些别名；它不会读取或暴露密钥。

---

## Quick start

### Install a release binary

每个 `v*` tag 会通过 GitHub Actions + GoReleaser 生成 darwin/linux、amd64/arm64
的 `tar.gz`、`checksums.txt` 和 GitHub Release：

```bash
gh release download --repo buerka/lemma --pattern 'lemma_*_darwin_arm64.tar.gz'
tar -xzf lemma_*_darwin_arm64.tar.gz
./lem version
# 压缩包同时包含 lem 和 lemma 两个二进制
```

也可以从源码构建。

### Build from source

```bash
git clone https://github.com/buerka/lemma.git
cd lemma
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o lem ./cmd/lem
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o lemma ./cmd/lemma

# or install both entrypoints
go install github.com/buerka/lemma/cmd/lem@latest
go install github.com/buerka/lemma/cmd/lemma@latest
```

一个二进制，一个命令；`lemma` 是完整别名：

```bash
lem          # CLI：人类、CI、任何能跑 shell 的 agent
lemma        # same CLI
```

`lem` 不实现 MCP；远端执行由 runner 子代理通过 ssh-mcp 完成。

### Initialize a research project

在项目根目录执行：

```bash
lem init \
  --project myproject \
  --host gpu \
  --workdir '~/project' \
  --run-root '~/.lem-runs' \
  --python python3
```

`lem init` 默认在当前目录创建 `progress/`；在其他目录初始化时加 `--root <path>`。
它也会创建 `progress/.git`，并写入第一条 `project.init` 事件与第一个 git commit。

生成：

```text
progress/
  .git/
  .gitignore
  project.yaml
  tasks.yaml
  events.jsonl
  gates.jsonl
  changesets/
  receipts/
  runs/
  STATE.md
  DECISIONS.md
```

需要多项目时，把当前项目注册到全局表：

```bash
lem project add myproject --root .
```

`project.yaml`：

```yaml
project: myproject
schema_version: 1
remote:
  transport: ssh-mcp
  host: gpu
  workdir: ~/project
  run_root: ~/.lem-runs
  python: python3
storage:
  artifact_root: ssh://gpu/~/.lem-runs
```

**Root vs workdir contract**

- `root`（`progress/` 的父目录）是**账本/项目元数据根**：`project.yaml`、
  `tasks.yaml`、`events.jsonl`、changesets、receipts 都在这里。
- `remote.workdir` 是**代码仓库根**，也是 runner 执行命令时的 cwd。
- `run start` 的 `codeInfo` 优先用 `remote.workdir` 找 git 仓库；找不到才回退 `root`。
- `--config` / `--data` 的相对路径先按 `remote.workdir` 解析，再回退 `root`；
  两处都找不到时，错误信息会同时列出尝试过的路径。
- 如果代码和账本在同一个目录，可把 `remote.workdir` 设为 `root` 或留空。

### Verify the setup

```bash
lem doctor
```

它会检查本地 Python/git、`project.yaml` 字段、`~/.ssh/config` 里的 host 别名，并打印 runner 应使用的 ssh-mcp 工具名。它不会自己连远端。

在 agent 里手动验证一次：

```text
ssh_hosts            -> 应该列出 gpu
ssh_exec {host: gpu, command: "uname -a"}
```

### Add a task

```bash
lem task add \
  --id T1 \
  --title "baseline reproduction" \
  --phase P0 \
  --owner agent \
  --acceptance "metrics.json 存在" \
  --acceptance "无 observation 泄漏"
```

### Start a run

```bash
lem run start --task T1 --method baseline --split random --seed 0
```

输出：

```text
progress/runs/T1.baseline.random.seed0/
  run_spec.json
  REMOTE_TASK.md
  runner.sh
  runner.py
```

`run start` 同时把任务置为 `doing`，记录 `code_commit`、dirty 时的
`code_patch_hash`、`config_hash` 和 `data_sha256`，并提交一个 `run.start` 事件。

把 `REMOTE_TASK.md` 交给 runner 子代理；它通过 ssh-mcp 执行，返回 `remote_result.json`。

### Finish a run

```bash
lem run finish T1.baseline.random.seed0 \
  --result progress/runs/T1.baseline.random.seed0/remote_result.json
```

### Review and apply a state change

```bash
lem plan --task T1 --status done \
  --evidence runs/T1.baseline.random.seed0/metrics.json \
  --out progress/changesets/cs-20261102-001.json

lem review progress/changesets/cs-20261102-001.json
# -> progress/REVIEW.md

# 人类确认后：
lem approve progress/changesets/cs-20261102-001.json --by human
lem apply progress/changesets/cs-20261102-001.json
# -> git commit + progress/receipts/cs-20261102-001.json

lem verify
```

`review` 刷新 `progress/REVIEW.md` 并提交；`apply` 先提交状态与事件，再提交
receipt（两个 commit），因此 `progress/` 始终是干净的，`verify` 能同时找到
事件链、gate 和 receipt。

### Resume a session

新会话的主 agent 只读三样东西：

```bash
lem status --json      # 当前阶段、活跃任务、阻塞、下一步
lem next --json        # 下一个可执行任务
cat progress/STATE.md   # 人类可读快照
```

---

## Directory layout

```text
lemma/                        # this tool
  README.md
  LICENSE
  go.mod
  go.sum
  .goreleaser.yaml
  .github/workflows/ci.yml
  .github/workflows/release.yml
  cmd/lem/main.go            # CLI entry
  cmd/lemma/main.go          # full alias
  internal/core/              # state machine, changeset, hash, git
  internal/cli/               # subcommands
  internal/schema/            # Go structs + validation
  internal/templates/         # typed renderers for the embedded templates
  schemas/
    task.schema.json
    run.schema.json
    changeset.schema.json
  templates/                  # embedded with embed.FS
    embed.go
    REVIEW.md.tmpl
    REMOTE_TASK.md.tmpl
    runner.py.tmpl
    runner.sh.tmpl
    STATE.md.tmpl

~/.config/lem/projects.yaml  # multi-project registry

<research-project>/           # any project
  progress/                   # runtime ledger, git-tracked
    .lock                     # per-project write lock
    project.yaml
    tasks.yaml
    events.jsonl              # append-only, hash-chained
    gates.jsonl               # human approvals
    changesets/
      cs-20261102-001.json
    receipts/
      cs-20261102-001.json
    runs/
      <run_id>/
        run_spec.json
        REMOTE_TASK.md
        runner.sh
        runner.py
        manifest.json
        metrics.json
        result.json
        remote_result.json
    STATE.md                  # generated
    REVIEW.md                 # generated, current review packet
    DECISIONS.md
```

大文件不在这里：

```text
<host>:<run_root>/<run_id>/
  stdout.log
  stderr.log
  exit_code
  checkpoints/
  plots/
  predictions/
```

---

## CLI

| Command | Purpose | Output |
|---|---|---|
| `lem init` | 初始化 `progress/` | `project.yaml`、`tasks.yaml`、`events.jsonl` |
| `lem project add/list/remove` | 管理多项目注册表 | `~/.config/lem/projects.yaml` |
| `lem doctor` | 检查本地配置和 host 别名 | 检查报告 |
| `lem task add/update/list` | 管理任务图 | `tasks.yaml` + task event |
| `lem run start` | 生成 run spec 和远端任务书 | `run_spec.json`、`REMOTE_TASK.md` |
| `lem run finish` | 接收 runner 结果 | `manifest.json`、`metrics.json`、run event |
| `lem run status` | 查询单次 run | 状态、日志尾部、exit code |
| `lem plan` | 生成变更单 | `changesets/cs-*.json` |
| `lem review` | 渲染人类审阅页 | `REVIEW.md` |
| `lem approve` | 人类批准变更哈希 | `gates.jsonl` |
| `lem apply` | 原子应用 + git commit | `receipts/cs-*.json` |
| `lem log` | 回溯查询 | Markdown / JSON |
| `lem show` | 查看单个 changeset / receipt | Markdown / JSON |
| `lem verify` | 校验哈希链、schema、git 状态 | exit code + report |
| `lem status` | 重新生成 `STATE.md` | 快照 |
| `lem next` | 返回下一个可执行任务 | task JSON |
| `lem artifact list` | 列出本地 manifest 中的文件与远端 artifact | 路径、URI、SHA-256 |
| `lem artifact verify` | 校验本地 manifest 文件哈希 | exit code + report |
| `lem artifact fetch` | v0.1 未实现，返回 `NOT_IMPLEMENTED` | — |
| `lem migrate` | 检查 schema 版本（v0.1 没有迁移） | 版本 |

所有命令支持 `--json` 和全局 `--project`；人类默认看到 Markdown。
`--json` 时成功结果写 stdout，错误对象也写 stdout；人类错误写 stderr。

退出码为 `0` ok、`1` general、`2` usage、`3` CONFLICT、`4` BUSY、
`5` IDEMPOTENT、`6` STALE。

---

## Data model

### Task

```yaml
- id: T1
  phase: P0
  title: baseline reproduction
  owner: agent            # agent | human
  status: doing           # todo | doing | blocked | done
  depends_on: []
  acceptance:
    - metrics.json 存在
    - 无 observation 泄漏
  artifacts: [progress/artifacts/splits/]
  updated_at: 2026-11-02T22:30:00+08:00
```

### Run

```json
{
  "run_id": "T1.baseline.random.seed0",
  "project": "myproject",
  "task_id": "T1",
  "method": "baseline",
  "split": "random",
  "seed": 0,
  "host": "gpu",
  "status": "succeeded",
  "code": {"repo": "git@github.com:you/project.git", "commit": "<sha>"},
  "config_hash": "sha256:...",
  "data": {"uri": "ssh://gpu/~/data/dataset.h5", "sha256": "..."},
  "metrics": {"macro_f2": 0.89},
  "artifacts": [
    {"name": "checkpoint", "uri": "ssh://gpu/~/.lem-runs/.../checkpoint.pt", "sha256": "..."}
  ]
}
```

### Event

`events.jsonl` 一行一个事件，带前序哈希：

```json
{"event_id":"ev-000123","ts":"2026-11-02T22:40:00+08:00","project":"myproject","type":"task.update","task_id":"T1","changeset_id":"cs-20261102-001","payload":{"from":"doing","to":"done"},"prev_hash":"sha256:...","hash":"sha256:..."}
```

`hash` 是去掉 `hash` 字段后的 canonical JSON 的 SHA-256；`revision` 就是最后一条事件的
`hash`。第一条事件的 `prev_hash` 为空。

### ChangeSet

```json
{
  "changeset_id": "cs-20261102-001",
  "project": "myproject",
  "task_id": "T1",
  "intent": "record baseline and mark task done",
  "base_revision": "sha256:...",
  "operations": [
    {"op": "run.append", "run_id": "T1.baseline.random.seed0"},
    {"op": "task.update", "id": "T1", "from": "doing", "to": "done"}
  ],
  "evidence": ["progress/runs/T1.baseline.random.seed0/metrics.json"],
  "risks": [],
  "rollback": "lem plan --revert cs-20261102-001",
  "prev_hash": "sha256:...",
  "hash": "sha256:..."
}
```

### Receipt

```json
{
  "changeset_id": "cs-20261102-001",
  "project": "myproject",
  "changeset_hash": "sha256:...",
  "applied_at": "2026-11-02T22:41:00+08:00",
  "git_commit": "a1b2c3d",
  "prev_commit": "<parent_sha>",
  "revision": "sha256:...",
  "status": "applied"
}
```

---

## Agent protocol

### Manager agent

主 agent 的职责是“派活、验收、更新状态”，不是写代码。

**Must**

1. 会话开始：`lem status --json` + `lem next --json`。
2. 只读 `STATE.md`、`TASKS.yaml`、`REVIEW.md`、runner 返回的 JSON。
3. 每个任务派给一个 worker 子代理，worker 自己读代码、跑实验。
4. worker 返回后，先校验 schema 和 acceptance，再 `lem plan`。
5. 会话结束：`lem status` + `lem verify`，确保状态已提交。

**Must not**

1. 不读 `src/`、`logs/`、完整 report；需要细节时让 worker 摘要。
2. 不直接编辑 `events.jsonl` / `tasks.yaml`。
3. 不直接 `git commit` / `git reset`；只通过 `lem apply`。
4. 不使用 ssh-mcp；远端由 runner 子代理负责。
5. 不自己写 approval；人类批准只能由 `lem approve --by human` 写入。

### Worker handoff

worker 必须返回固定 schema：

```json
{
  "task_id": "T2",
  "status": "succeeded",
  "summary": "energy score improves AUROC by 0.03 over softmax max",
  "changes": [
    {"path": "train.py", "kind": "modified", "diff_stat": "+42/-7"}
  ],
  "evidence": {
    "run_ids": ["T2.energy.obs.seed0"],
    "metrics": {"auroc": 0.81, "unknown_recall": 0.61},
    "artifacts": ["runs/T2/..."]
  },
  "risks": ["unknown class support is small; CI is wide"],
  "rollback": "git revert <sha>",
  "next_steps": ["rerun on time split"],
  "confidence": "high"
}
```

主 agent 只接受 schema 合法、evidence 齐全的 handoff；不合法就退回。

### Human gates

人类只在四个点介入：

| Gate | 问题 | 证据 |
|---|---|---|
| G1 Split | 随机 / 观测分组 / 时间顺序是否合理？ | split manifest + 哈希 |
| G2 Unknown | unknown 类怎么构造、支撑数多少？ | 协议草案 + per-class support |
| G3 Full run | 是否允许全量训练？预算多少？ | pilot 结果 + 预算 |
| G4 Conclusion | 这个结论是否有足够证据？ | run 表 + 置信区间 |

批准记录写入 `gates.jsonl`，包含 `changeset_hash`、`approved_by`、`approved_at`。

---

## Remote execution with ssh-mcp

`lem` **不实现 SSH**。远端执行由 runner 子代理通过 ssh-mcp 完成，transport 与状态机分离。

### ssh-mcp tool contract

| Operation | Tool | Notes |
|---|---|---|
| list hosts | `ssh_hosts` | reads `~/.ssh/config` aliases |
| run command | `ssh_exec` | returns exit code + text; 8 KB cap by default |
| page output | `ssh_output` | fetch the truncated middle by id |
| upload file | `ssh_put` | SFTP |
| download file | `ssh_get` | SFTP |
| list directory | `ssh_ls` | SFTP |
| port forward | `ssh_forward` | localhost only |

`ssh_exec` 默认超时 60 秒；长任务用 `tmux` 启动后轮询，不要阻塞等待。

### Contract

```text
lem run start
  -> progress/runs/<run_id>/run_spec.json
  -> progress/runs/<run_id>/REMOTE_TASK.md

Manager Agent
  -> spawn runner subagent with REMOTE_TASK.md

Runner Subagent (ssh-mcp only)
  -> ssh_exec(host, "mkdir -p <run_root>/<run_id>")
  -> ssh_put(host, run_spec.json -> <run_root>/<run_id>/run_spec.json)
  -> ssh_exec(host, "tmux new-session -d -s <run_id> 'bash <run_root>/runner.sh <run_id>'")
  -> poll: ssh_exec(host, "cat <run_root>/<run_id>/exit_code")
  -> ssh_get(host, manifest.json / metrics.json / result.json / plots/)
  -> write progress/runs/<run_id>/remote_result.json

Manager Agent
  -> lem run finish <run_id> --result remote_result.json
```

### `run_spec.json`

```json
{
  "run_id": "T1.baseline.random.seed0",
  "project": "myproject",
  "task_id": "T1",
  "host": "gpu",
  "workdir": "~/project",
  "run_root": "~/.lem-runs",
  "python": "python3",
  "command": "python train.py --config configs/baseline.yaml --seed 0",
  "env": {"PYTHONHASHSEED": "0"},
  "inputs": [
    {"path": "~/data/dataset.h5", "sha256": "..."}
  ],
  "outputs": ["metrics.json", "checkpoints/"]
}
```

### `REMOTE_TASK.md` template

```md
# Remote task: <run_id>
Host: <host>
Remote dir: <run_root>/<run_id>
Local spec: progress/runs/<run_id>/run_spec.json

## Steps
1. ssh_exec: mkdir -p <remote_dir>
2. ssh_put: run_spec.json -> <remote_dir>/run_spec.json
3. ssh_exec: tmux new-session -d -s <run_id> 'bash <run_root>/runner.sh <run_id>'
4. Poll every 60s: cat <remote_dir>/exit_code; tail -n 20 stdout.log
5. On exit: ssh_get manifest.json, metrics.json, result.json, plots/
6. Write progress/runs/<run_id>/remote_result.json

## Rules
- Use the ssh-mcp tools exposed by your client (names may be prefixed).
- Do not use raw ssh/scp; ssh-mcp is the only transport.
- Do not modify progress/events.jsonl or tasks.
- Do not download checkpoints.
- If failed: return exit_code + logs_tail + error; do not retry without instruction.
```

### `runner.sh` template

```bash
#!/usr/bin/env bash
set -euo pipefail

RUN_ID="$1"
REMOTE_ROOT="${LEM_REMOTE_ROOT:-$HOME/.lem-runs}"
WORKDIR="${LEM_WORKDIR:-$HOME/project}"
PYTHON="${LEM_PYTHON:-python3}"
RUN_DIR="$REMOTE_ROOT/$RUN_ID"

cd "$WORKDIR"
"$PYTHON" runner.py --spec "$RUN_DIR/run_spec.json" \
  > "$RUN_DIR/stdout.log" 2>&1
echo $? > "$RUN_DIR/exit_code"
```

`runner.py` 读 `run_spec.json`，校验输入哈希，执行 `command`，把输出哈希写进 `manifest.json`。它不依赖 `lem`，可以单独放在远端。

### Why not put SSH in `lem`

- 认证、host alias、known_hosts 只维护一份（ssh-mcp）。
- 主 agent 的上下文里不会出现 SSH 命令和远端日志。
- 以后换 Slurm / HTTP / 第二台 GPU，只换 runner 子代理，`lem` 不动。
- 其他 agent（Codex、Claude Code、Cursor）只要接同一个 ssh-mcp，就能复用同一套 runner 提示词。

---

## Storage

### Control plane

`progress/` 很小，进 git：

- `project.yaml`、`tasks.yaml`
- `events.jsonl`
- `changesets/`、`receipts/`
- `runs/<run_id>/manifest.json`、`metrics.json`、`result.json`
- `STATE.md`、`REVIEW.md`、`DECISIONS.md`

### Data plane

大文件留在远端 `<run_root>/<run_id>/`：

```text
stdout.log
stderr.log
exit_code
checkpoints/
plots/
predictions/
```

### Rules

- 数据集只保留一份 canonical copy；首次 `sha256sum` 后写进 `project.yaml`。
- 每次 run 只校验数据集哈希，不复制。
- checkpoint、日志、预测不拉回本地；`lem artifact fetch` 在 v0.1 尚未实现。
- artifact 用 `URI + SHA-256` 引用，不用本地绝对路径；`lem artifact list/verify`
  只校验本地 manifest，远端对象显示为 `remote`。
- `lem gc` 尚未实现。

---

## Reproducibility

每个 run 必须记录：

| Field | How |
|---|---|
| `code_commit` | `git -C <remote.workdir or root> rev-parse HEAD`（远端 runner 会重新记录） |
| `code_patch_hash` | 本地/远端 `git diff` + `git status` 的 SHA-256（dirty 时） |
| `config_hash` | canonical JSON 或配置文件字节的 SHA-256 |
| `data_sha256` | 本地 `sha256sum <data_path>`；远端 URI 由 `--data-sha256` 提供 |
| `split_hash` | split manifest hash |
| `environment_hash` | `pip freeze` + `nvidia-smi` hash |
| `seed` | run spec |
| `metrics` | `metrics.json` |
| `artifacts` | URI + SHA-256 |

`lem artifact verify` 检查本地 manifest 的文件是否缺失、SHA-256 是否匹配；
`lem verify` 负责 schema、事件链、gate、receipt 和 git 状态。

---

## Query and rollback

### Read-only traceback

```bash
lem log --task T2
lem log --since 2026-10-26
lem log --type gate
lem log --grep "CUDA OOM"
lem show cs-20261102-001
```

人类直接对主 agent 说：

- “T2 为什么失败？”
- “上周改了什么？”
- “这个 0.89 是哪次 run？”
- “回到 T1 批准前。”

主 agent 把这些翻译成 `lem log` / `lem show`，只返回摘要和链接，不返回原始日志。

### Rollback

回滚不是 `git reset`，而是生成反向 changeset：

```bash
lem plan --revert cs-20261102-001 --out progress/changesets/cs-20261102-002.json
lem review progress/changesets/cs-20261102-002.json
# human approve
lem approve progress/changesets/cs-20261102-002.json --by human
lem apply progress/changesets/cs-20261102-002.json
```

历史保留，审计链不断。

---

## Agent compatibility

| Agent | Run `lem` | ssh-mcp for remote | Notes |
|---|---|---|---|
| ZCode | shell / Bash | yes | MCP config with `command: /path/to/ssh-mcp` |
| Codex | shell | yes | `~/.codex/config.toml` |
| Claude Code | shell | yes | `claude mcp add ssh -- /path/to/ssh-mcp` |
| Cursor | shell | yes | MCP settings |
| Other MCP client | shell | yes | add ssh-mcp as a stdio MCP server |
| Agent without MCP | shell | no | `lem` works locally; remote needs manual `ssh`/`sftp` |

`lem` 本身是普通 CLI，不需要任何 MCP；只有远端执行依赖 ssh-mcp。

---

## Implementation notes (Go)

- **Shell out to git**, don't use `go-git`. `exec.Command("git", "-C", repo, ...)`; exit codes and output are what matter.
- **Canonical JSON** for changeset and event hashing: sorted keys, no insignificant whitespace, UTF-8, exact numbers. Hash the bytes with `crypto/sha256`.
- **Atomic writes**: temp file in the same directory, `fsync`, `os.Rename`, directory `fsync`; keep a `.lock` file for single-writer.
- **Locks**: in-process mutex plus `flock` per ledger (`github.com/gofrs/flock`), so goroutines and processes exclude each other.
- **Templates**: `text/template` + `embed.FS` for `REVIEW.md`, `REMOTE_TASK.md`, `STATE.md`, `runner.sh`, `runner.py`.
- **YAML**: `gopkg.in/yaml.v3`; keep `project.yaml` / `tasks.yaml` structs explicit.
- **Logging**: CLI output to stdout; errors to stderr. No background logging.
- **Tests**: `go test -race ./...`; unit tests for canonical hashing and event chains; integration tests use temp git repos, 10 concurrent applies, two projects, and an injected apply failure.
- **Release**: GoReleaser v2 builds `lem` and `lemma` with `CGO_ENABLED=0` for
  darwin/linux amd64/arm64, writes `checksums.txt`, and publishes a GitHub
  Release from a `v*` tag.

### CI and release process

- `ci.yml` runs on every push/PR for Ubuntu and macOS, Go 1.22.x and stable:
  `gofmt -l`, `go vet`, `go test -race`, `go build`, plus a GoReleaser `check`
  and a full `--snapshot` cross-compile.
- `release.yml` runs on `v*` tags: re-runs the race tests, then GoReleaser
  publishes the archives, checksums, and GitHub Release.
- Dependabot checks Go modules and GitHub Actions weekly.
- Release command:

  ```bash
  git tag -a v0.1.0 -m "Lemma v0.1.0"
  git push origin v0.1.0
  ```

### Implementation status and known limits

Implemented in v0.1:

- `init`, `doctor`, `task add/update/list`, `run start/finish/status`,
  `status`, `next`, `plan`, `review`, `approve`, `apply`, `log`, `show`,
  `verify`, `project add/list/remove`, `artifact list/verify`, `migrate`.
- Per-project registry, `--project`, `LEM_PROJECT`, and cwd discovery.
- Hash-chained `events.jsonl`, changesets, gates, receipts, independent
  `progress/` git repo, atomic writes, and `BUSY/CONFLICT/IDEMPOTENT/STALE`
  semantics.
- Local run manifests and artifact listing/verification.

Not implemented in v0.1:

- `artifact fetch`, `gc`, MCP server, direct SSH, dashboard/TUI, DVC/MLflow,
  remote GC, multi-user permissions, automatic scientific judgment.
- `lem migrate` only reports the schema version; there are no older schemas to
  migrate from yet.
- `apply` commits the receipt in a second commit after the state/event commit.
  This keeps the ledger clean and the receipt auditable without rewriting
  history.

---

## Non-goals

- 不做任务调度器；GPU 排队用 tmux / Slurm。
- 不做实验追踪 UI；需要时把 `metrics.json` 导入 MLflow。
- 不做数据版本控制；需要时加 DVC，但 `lem` 不依赖它。
- 不做多用户权限系统；当前假设单用户 + 单主 agent。
- 不做自动科学判断；gate 必须由人批准。

---

## Roadmap

- **v0.1 (this repo)**：Go core + CLI + 单/多项目 + 锁/revision + 哈希链 +
  `plan/review/approve/apply` + receipt/gate + `BUSY/CONFLICT/IDEMPOTENT/STALE` +
  ssh-mcp runner 模板 + `remote_result.json` 入库 + `artifact list/verify`。
- **v0.2**：`artifact fetch` + 远端 GC。
- **v0.3**：可选静态 `dashboard.html`（只读，不引入 server）。

---

## Contributing

这个项目的目标是“薄”。提交 PR 前请先问：

1. 这个功能未来 3 个月会用吗？
2. 它会增加新的状态源吗？
3. 它能让主 agent 少读一个文件吗？

如果答案分别是“否 / 是 / 否”，大概率不该加。

---

## License

MIT
