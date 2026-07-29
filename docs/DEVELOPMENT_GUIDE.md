# CEE 开发文档

面向"要在这个代码库上做开发"的人——无论是修改引擎本身，还是接入一个新的领域插件。架构原理见 `TECHNICAL_SPECIFICATION.md`，贡献规则见 `NORMATIVE_HANDBOOK.md`，本文档只讲"怎么动手"。

## 1. 环境要求

- Go 1.22 及以上（`go.mod` 当前声明 `go 1.26.5`，跟随开发机实际工具链版本，向下兼容到 1.22 语法即可）
- 无需任何外部依赖——`go.mod` 目前没有 `require` 条目，`go build`/`go test` 在无网络环境下也能跑

```bash
go version        # 确认 >= 1.22
go build ./...     # 编译全部包
go vet ./...        # 静态检查
go test ./... -v    # 跑全部测试
```

## 2. 项目结构

```
cee/
  go.mod
  entities/      共享数据契约，所有组件都依赖它，它不依赖任何其他包
  intentrouter/   意图路由
  execution/      确定性执行引擎（DAG 走法、断路器）
  llminjector/    边缘 LLM 抽取器
  sandbox/        预执行沙盒
  registry/       领域注册表（把插件接入 Router + Engine）
  manifest/       JSON 声明式加载器（Load 出 registry.Domain）+ 静态校验器（Validate）
  stdlib/         标准动作库（std.set / std.require / std.rule_check）
  scorecard/      度量一次请求：确定性步数 / LLM 调用 / 沙盒 / 断路器 / 耗时
  catalog/        社区分发层：index.json + plugins/<name>/manifest.json (+ benchmark.json)
  bench/          基准跑批：把标准事件跑过插件，聚合 Scorecard 并排名
  cmd/cee/        命令行工具：validate / lint / list / install / bench
  docs/           本文档所在目录
  examples/
    security_monitoring/   L2 范例：Go 插件 + 沙盒门禁 + 断路器降级人工审批
    manifests/             L1 范例：expense-guard.json，纯 JSON、零 Go 代码
  tests/          Go 惯例是 *_test.go 跟源码同目录，这个顶层目录目前未使用
```

包之间的依赖方向是单向的，不存在循环导入：

```
entities  ←  intentrouter, execution, llminjector, sandbox
execution ←  registry, manifest, stdlib
intentrouter ← registry, manifest
registry  ←  manifest
stdlib    ←  manifest
manifest, stdlib  ←  cmd/cee
```

`scorecard` 是叶子包：它不 import 任何其他 cee 包，而是靠方法集结构化地满足 `execution.Observer` 和 `llminjector.Observer` 两个接口，所以埋点不会造成 `scorecard → execution` 的反向依赖。

## 3. 快速开始：从零跑通一个领域

有两种等价的方式定义一个领域插件，最终都产出同一个 `registry.Domain`，可以混用。

### 方式一：手写 Go 结构体（适合需要复杂 Go 逻辑的场景）

```go
package main

import (
    "github.com/cee-project/cee/entities"
    "github.com/cee-project/cee/execution"
    "github.com/cee-project/cee/intentrouter"
    "github.com/cee-project/cee/registry"
)

func main() {
    router := intentrouter.NewRouter(0.5) // 阈值按场景调
    engine := execution.NewEngine(nil)     // 无沙盒依赖时传 nil
    reg := registry.NewRegistry(router, engine)

    reg.RegisterDomain(registry.Domain{
        Name: "finance",
        Intents: []entities.IntentNode{{
            NodeID:       "finance.duplicate_expense",
            DomainID:     "finance",
            Examples:     []string{"duplicate expense report"},
            EntryWorkflowRef: "finance.flag_duplicate", // 指向下面某个 Workflow 的 WorkflowID
        }},
        Workflows: []*execution.Workflow{{
            WorkflowID:  "finance.flag_duplicate",
            EntryStepID: "check",
            Steps: map[string]execution.Step{
                "check": &execution.LeafStep{
                    StepID: "check",
                    Run: func(ctx map[string]any) (map[string]any, error) {
                        return map[string]any{"flagged": true}, nil
                    },
                },
            },
        }},
    })

    match := router.Match("finance", "duplicate expense report submitted again")
    if match.Matched {
        result, _ := engine.Run(match.EntryWorkflowRef, map[string]any{})
        _ = result // result.Output["flagged"] == true
    }
}
```

### 方式二：JSON manifest + 具名 Hooks（适合把 DAG 形状交给非 Go 开发者维护）

1. 写一个 manifest 文件（结构参考 `manifest/manifest_test.go` 里的 `financeManifestJSON`）：

```json
{
  "name": "finance",
  "intents": [
    {"node_id": "finance.duplicate_expense", "examples": ["duplicate expense report"], "entry_workflow_ref": "finance.flag_duplicate"}
  ],
  "policies": [
    {"policy_id": "escalate_to_review", "fallback_step_ref": "human_review"}
  ],
  "workflows": [{
    "workflow_id": "finance.flag_duplicate",
    "entry_step_id": "check",
    "steps": [
      {"step_id": "check", "type": "leaf", "action_ref": "finance.check_duplicate",
       "circuit_breaker_policy_ref": "escalate_to_review", "on_success": "notify"},
      {"step_id": "notify", "type": "leaf", "action_ref": "finance.notify_finance_team"},
      {"step_id": "human_review", "type": "leaf", "action_ref": "finance.queue_human_review"}
    ]
  }]
}
```

2. 在 Go 代码里只写"具名函数"，不写 DAG 结构：

```go
hooks := manifest.Hooks{
    "finance.check_duplicate":     checkDuplicateAction,
    "finance.notify_finance_team": notifyAction,
    "finance.queue_human_review":  queueHumanReviewAction,
}

// 第三个参数是标准动作库；manifest 里的 action_ref 会先在标准库里找，
// 找不到再在 hooks 里找。两者都可为 nil。
domain, err := manifest.Load(manifestJSONBytes, hooks, stdlib.Default())
if err != nil {
    // manifest 引用了一个标准库和 hooks 里都不存在的 action_ref，或 JSON 格式错误，
    // 或 composite step 缺 sub_workflow_ref —— Load 会明确报错，不会静默生成半个 Domain
}
reg.RegisterDomain(*domain)
```

### 方式二·补充：纯声明式（零 Go）

如果一个流程只用到标准动作库里的通用动作（`std.set`/`std.require`/`std.rule_check`），那么 `hooks` 传 `nil` 即可，插件作者一行 Go 都不用写——这就是社区 L1 贡献层。标准动作靠 manifest 里每个 step 的 `with` 块传参：

```json
{"step_id": "check_threshold", "type": "leaf", "action_ref": "std.require",
 "with": {"field": "amount", "op": "lte", "value": 10000},
 "circuit_breaker_policy_ref": "route_to_flag", "on_success": "approve"}
```

`std.require` 是无 if/else 引擎里表达分支的惯用法：条件成立则走 `on_success`，不成立则**失败**，从而经由 `circuit_breaker_policy_ref` 路由到 fallback step。完整可运行范例见 `examples/manifests/expense-guard.json`。

### 提交前用 `cee validate` 自查

`cmd/cee` 提供一个静态校验器，把结构完整性和引用完整性检查做成一条命令，纯声明式 manifest 可被完整校验（引用了自定义 Go hook 的 step 只能结构校验，hook 是否存在由 `Load` 在运行时兜底，validate 会以 warning 提示）：

```bash
go run ./cmd/cee validate examples/manifests/expense-guard.json
# ok: no issues        -> exit 0
# [error] ...           -> exit 1（可直接用于 CI 门禁）
```

它会抓出：悬空的 `on_success`/`sub_workflow_ref`/`entry_workflow_ref`、引用了未声明的 `circuit_breaker_policy_ref`、断路器 fallback 指向不存在的 step、标准动作参数写错、重复 step_id、以及会让 `Engine.Run` 失控的 `on_success` 环 / `sub_workflow_ref` 环等。

### 把插件发布到 catalog（社区分发）

`catalog/` 是一个 git-based 的插件目录——`index.json` 加它指向的 manifest 文件，贡献一个 L1 插件就是一个 PR：

```bash
go run ./cmd/cee list             # 列出 catalog 里的插件
go run ./cmd/cee lint             # 校验整个 catalog（CI 门禁；exit 1 = 有问题）
go run ./cmd/cee install sla-guard # 校验通过后把 manifest 拉进 ./plugins/
```

发布步骤：在 `catalog/plugins/<name>/manifest.json` 放你的 manifest，在 `catalog/index.json` 加一条 entry（`name`/`tier`/`version`/`domain`/`manifest` 路径），跑 `cee lint` 确认干净即可提 PR。`install` 是**先校验再落盘**——过不了 `cee validate` 的插件不会被装上。需要 Go Hook 的 L2 插件走 Go module 分发，不通过 catalog 的 `install`（但可以在 index 里用 `tier: "L2"` 登记以便被发现）。

想上排行榜：再加一个 `benchmark` 字段指向 `plugins/<name>/benchmark.json`（一组 `{workflow_ref, context}` 标准事件），然后：

```bash
go run ./cmd/cee bench             # 跑所有带 benchmark 的插件，按确定性比率排名
```

排名口径是"相比每步一次 LLM 的 Agent 消除了多少调用"，这就是让贡献者为了一个可炫耀的数字去优化流程的社会化机制。

`action_ref` 在 `hooks` 里找不到、`type` 既不是 `"leaf"` 也不是 `"composite"`、`composite` 缺 `sub_workflow_ref`——这三类错误 `Load` 都会返回带上下文（域名/workflow名/step名）的 `error`，方便定位是哪个 manifest 的哪个 step 写错了。

## 4. 如何给一个 Step 接入沙盒门禁

在 `LeafStep` 上填 `SandboxProbeRef`，并向传给 `execution.NewEngine` 的 `sandbox` 注册同名探针：

```go
sb := sandbox.NewSandbox()
sb.RegisterProbe("check_impact", func(ctx map[string]any) (bool, string, error) {
    // 只做只读/模拟检查，绝不能有真实副作用
    if ctx["target_host"] == "dc01" {
        return false, "would isolate a domain controller", nil
    }
    return true, "", nil
})

engine := execution.NewEngine(sb) // Sandbox 满足 execution.Prober 接口
```

`Engine.Run` 会在执行该 Step 的 `Run` 之前先调用这个探针；探针返回 `healthy=false` 时，Step 的真实动作**不会执行**，直接走该 Step 声明的 `CircuitBreakerPolicyRef`。

## 5. 如何给一次抽取接入 schema 校验

```go
inj := llminjector.NewInjector()
inj.RegisterSchema("finance.expense_fields",
    llminjector.Schema{"amount": llminjector.FieldFloat64, "category": llminjector.FieldString},
    func(rawText string) (map[string]any, error) {
        // 这里调真实的小模型/规则做抽取，返回值可以包含多余字段——
        // Extract 只会保留 schema 里声明过的那些
        return callYourLLM(rawText)
    },
)

result := inj.Extract(entities.ExtractionRequest{
    RawText: "taxi to airport $4200", SchemaRef: "finance.expense_fields", DomainID: "finance",
})
if result.Success {
    amount := result.StructuredPayload["amount"].(float64)
}
```

## 5b. 如何度量一次请求（Scorecard）

`scorecard.Recorder` 是 per-request 的：每次请求 new 一个,attach 到引擎(和注入器),跑完 `Snapshot`。引擎默认不带 observer,零开销;只有 attach 后才回调。

```go
recorder := scorecard.NewRecorder()
engine.SetObserver(recorder)          // 引擎侧:步数/沙盒/断路器
injector.SetObserver(recorder)         // 注入器侧:LLM 抽取次数(有 LLM 环节时才需要)

result, _ := engine.Run(entryRef, ctx)

card := recorder.Snapshot(entryRef)
fmt.Println(card)                       // determinism 100% (2 deterministic steps, 0 LLM calls)...
_ = card.DeterminismRatio()             // 0.0~1.0,= 相比"每步一次 LLM"的 Agent 省掉的调用比例
```

被计数的是**实际执行**:被沙盒拦下、动作没跑成的 Step 不算确定性步数(算沙盒预演 + 断路器)。`examples/security_monitoring` 里有完整用法。

## 6. 各包 API 速查

| 包 | 构造函数 | 关键方法 |
|---|---|---|
| `intentrouter` | `NewRouter(threshold float64) *Router` | `RegisterNode(entities.IntentNode)`、`Match(domainID, rawText string) entities.MatchResult` |
| `execution` | `NewEngine(sandbox Prober) *Engine` | `RegisterWorkflow(*Workflow)`、`RegisterPolicy(CircuitBreakerPolicy)`、`SetObserver(Observer)`、`Run(workflowRef string, ctx map[string]any) (entities.WorkflowResult, error)` |
| `llminjector` | `NewInjector() *Injector` | `RegisterSchema(schemaRef string, schema Schema, extractor Extractor)`、`SetObserver(Observer)`、`Extract(entities.ExtractionRequest) entities.ExtractionResult` |
| `sandbox` | `NewSandbox() *Sandbox` | `RegisterProbe(probeRef string, probe Probe)`、`Probe(entities.ProbeRequest) (entities.ProbeResult, error)` |
| `registry` | `NewRegistry(router *intentrouter.Router, engine *execution.Engine) *Registry` | `RegisterDomain(Domain)`、`Domains() []string` |
| `scorecard` | `NewRecorder() *Recorder` | `SetObserver` 可接受它；`Snapshot(workflowID string) Scorecard`、`Scorecard.DeterminismRatio()` |
| `stdlib` | 无（`Default() Library` 返回内置动作） | 内置 `std.set`、`std.require`、`std.rule_check` |
| `manifest` | 无（纯函数包） | `Load(data []byte, hooks Hooks, std stdlib.Library) (*registry.Domain, error)`、`Validate(data []byte, std stdlib.Library) Report` |

## 7. 测试规范（怎么写，不是要不要写——那是 NORMATIVE_HANDBOOK 的事）

- Go 惯例：`xxx_test.go` 跟被测代码放在同一目录、同一个包名下（除 `registry_test.go`/`manifest_test.go` 这类需要验证"多个包协作"的集成测试外，一般不需要额外的 `_test` 后缀包）。
- 优先用标准库 `testing` + `errors.As`/`errors.Is`，不引入第三方断言库——保持模块零依赖。
- 每个包至少要覆盖：一条正常路径、一条边界/未注册引用的报错路径。`execution` 包额外要覆盖断路器命中和沙盒门禁两条路径（参考 `engine_test.go` 里 `TestFailureWithPolicyFallsBack`、`TestSandboxGateBlocksUnhealthyStepViaCircuitBreaker`）。

## 8. 常见问题

- **`engine.Run` 报 `no workflow registered for "xxx"`**：`WorkflowID` 和 `IntentNode.EntryWorkflowRef` 必须对得上——传给 `Run` 的就是 *WorkflowID*。
- **`cee validate` 报 `entry_step_ref ... is deprecated`**：这个字段改名了。它装的一直是 `workflow_id`，而旧名字 `entry_step_ref` 读起来像在指一个 step，是纯粹的错名。改法就是把 JSON 里的键名换掉，值一个字不用动：

  ```diff
  - {"node_id": "finance.dup", "entry_step_ref": "finance.flag_duplicate"}
  + {"node_id": "finance.dup", "entry_workflow_ref": "finance.flag_duplicate"}
  ```

  旧名仍然可用（第 3 条不允许删除已发布的 JSON 字段），只是会告警。两个名字都写且值不同则直接报错——那种情况没法在不猜作者意图的前提下解决。Go 那边字段已经是 `EntryWorkflowRef`，旧名不再存在，编译期就会提示。
- **`sandbox_probe_ref` 声明了但没注册探针**：`Engine.Run` 会返回 `no probe registered for "xxx"` 的 error，而不是静默跳过门禁——这是故意的,不允许"声明了门禁但门禁形同虚设"。
- **manifest 加载报 `references unregistered action_ref`**：检查 `Hooks` map 的 key 是否跟 JSON 里的 `action_ref` 完全一致（区分大小写）。
