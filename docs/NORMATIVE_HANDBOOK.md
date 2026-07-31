# CEE 规范性开发手册

> English: [NORMATIVE_HANDBOOK.en.md](NORMATIVE_HANDBOOK.en.md)

本手册是强制性规则，不是建议。区别在于：`DEVELOPMENT_GUIDE.md` 教你怎么做，本手册规定什么**不允许**做，以及为什么——因为 CEE 定位是任何行业都能接入的开源协议，规则必须写死在文档里被所有贡献者共同遵守，而不能靠"大家都懂行业惯例"这种默契。

每条规则标注了违反后果，供 Code Review 时引用。

## 1. 架构红线（违反即拒绝合并）

### 1.1 LLM 只能抽取，不能决策

`llminjector.Extractor` 的返回值只允许包含"从原文里抽出来的事实性字段"（金额、日期、实体名称……），**不允许包含任何"下一步该怎么办"性质的字段**（是否异常、是否放行、严重等级……）。

- **为什么**：这是 CEE 区别于"LLM 全权 Agent"的核心边界。一旦决策权混进抽取结果，确定性执行引擎就退化成了"LLM 说了算，代码只是壳子"。
- **强制机制**：`Injector.Extract` 只拷贝 `Schema` 里声明过的字段到 `StructuredPayload`，schema 之外的字段会被静默丢弃（见 `injector.go` 的 `clean` 构造逻辑）。**因此这条规则实际上是靠"不要在 Schema 里声明决策字段"来落地的**——Code Review 时如果看到一个 `Schema` 里出现 `is_valid`、`should_alert`、`severity_level` 这类字段名，必须打回。
- 合法字段名的形状：名词性、可从原文直接验证真伪（`amount`、`merchant_name`、`invoice_id`）。非法字段名的形状：判断性、需要业务规则才能得出结论（`is_fraud`、`risk_score`、`action`）。

#### 1.1.1 例外澄清：`std.rule_check` 为什么可以产出判断字段

`stdlib.std.rule_check` 会往输出里写 `is_high_value` 这类字段名——形状上正好落在上一条列出的"非法字段名"里。这不是违规，但**必须理解清楚豁免的边界在哪，否则这条例外会被用来架空 1.1**：

- 1.1 约束的是**谁做的判断**，不是**判断长什么样**。禁止的是"判断由 LLM 做出、代码照单全收"；`std.rule_check` 的判断由 manifest 里写死的 `field / op / value` 三元组决定，是纯代码、完全可复现、可静态审计（`cee validate` 就能看出它比的是什么），跟 LLM 无关。
- 因此判据是**这个字段的值从哪来**：
  - 来自 `llminjector` 的 `Schema` → 适用 1.1，判断字段一律打回。
  - 来自 `stdlib` 或领域 Go Hook 的确定性计算 → 允许，因为它就是"业务规则"本身，而业务规则本来就该由代码承担。
- **禁止的组合**：用 `llminjector` 抽出一个事实字段（合法），再让 `std.rule_check` 去比对它——这本身没问题；但**不允许**让 Extractor 直接输出一个已经算好的判断结果，再用 `std.set` 原样搬进 context 来"洗白"。Code Review 时如果看到 `std.set` 的 `fields` 值不是字面常量而是来自抽取结果的判断字段，按违反 1.1 处理。

#### 1.1.2 抽取出来的是"猜测"，不是"事实"

1.1 挡住了"LLM 说该怎么办"，但挡不住**抽错一个数**。一个把 $50,000 读成 $5,000 的抽取器什么决定都没做，却又什么都决定了——下游的确定性规则会非常自信地自动放行。

因此 `Injector.Extract` **结构性地**给它产出的每个字段打上来源标记（`ExtractionResult.ModelDerived`），抽取器无法豁免。`llminjector.ContextFrom` 负责让这个标记跟着值一起进入 workflow context（key 为 `cee.model_derived`）。

- **为什么不是置信度分数**：模型自报的置信度引擎无法审计，而**一个没人能核实的数字比没有数字更糟——它制造虚假的安心**。"这个值是不是模型猜的"则是一个在产生的那一刻就百分之百确定的结构性事实。
- **有后果的 Step 必须自己挡**：转账、隔离主机、停用账号这类步骤，应当用 `std.require_verified` 声明哪些字段不接受猜测值，失败经断路器转人工：

  ```json
  {"action_ref": "std.require_verified", "with": {"fields": ["amount", "account"]},
   "circuit_breaker_policy_ref": "needs_human_check"}
  ```

- **禁止洗白（对应 1.1.1 同一类漏洞）**：**不存在也不允许新增任何"把字段标记为已核实"的标准动作**。manifest 里能盖"已核实"的图章就是一件洗钱工具——抽一个数、盖个章、照着执行。把一个值从"猜测"提升为"事实"只能由 Go Hook 完成，且必须是**真的**去对了权威系统或问了人之后。Code Review 时看到任何直接改写 `cee.model_derived` 的 Hook，按违反本条处理。
- **调用方不得手动 merge 抽取结果**：`ContextFrom` 存在的唯一理由就是让正确做法比错误做法更省事。直接把 `StructuredPayload` 拷进 context 会静默丢掉来源，之后 `std.require_verified` 就形同虚设。

### 1.2 Sandbox Probe 必须只读/模拟，不能有真实副作用

`sandbox.Probe` 注册的函数运行在"预演"阶段，**在真实 Step 执行之前**。探针内部不允许调用任何会修改外部状态的 API（转账、发通知、改配置、写数据库……）。

- **为什么**：沙盒存在的意义是"先模拟再决定要不要真做"；如果探针本身有副作用，"预演"这个概念就不成立了，等于执行了两次。
- **Code Review 检查点**：任何 Probe 函数体内出现 `POST`/`PUT`/`DELETE` 语义的调用，或任何写数据库操作，直接打回。允许的操作：只读 API 调用、连通性检查、dry-run 模式的 API（前提是该 API 的 dry-run 参数被第三方证实不产生副作用）。

### 1.3 断路器策略必须以命名引用声明，不允许内联

`LeafStep`/`CompositeStep` 的 `CircuitBreakerPolicyRef` 必须指向一个通过 `Engine.RegisterPolicy` 注册的策略。**不允许**在 `Action` 函数内部手写 `for`/重试循环、`time.Sleep` 退避、或者硬编码的 fallback 逻辑来绕开这个机制。

- **为什么**：策略集中注册是"全局可审计"的前提——治理者需要能回答"系统里一共有多少条安全网、分别兜底到哪"，如果重试逻辑散落在各个 `Action` 函数体内部，这个问题永远答不出来。
- **Code Review 检查点**：`Action`/`Run` 函数体内出现循环 + `time.Sleep`/重试计数器组合模式，视为违规,应该重构成"失败就返回 error，由 `CircuitBreakerPolicyRef` 接管"。

### 1.4 引擎包本身不允许出现行业逻辑

`entities`、`execution`、`intentrouter`、`llminjector`、`sandbox`、`registry`、`manifest`、`stdlib` 这八个包，**任何时候都不允许 import 或硬编码某个具体行业的概念**（不能出现 `if domainID == "finance"` 这类分支，不能有字段叫 `InvoiceAmount` 而不是通用的 `map[string]any`）。

`stdlib` 尤其要守住这条：标准动作库天然会被"再加一个动作就能支持某某场景"的需求拉扯。判据是**动作名和参数里不能出现任何行业名词**——`std.require` 合法（它只知道"字段、运算符、值"），一个假想的 `std.check_invoice_total` 则非法，那属于领域插件自己的 Go Hook。

- **为什么**：这是"引擎只认引用不认内容"的字面意义。一旦某个行业的假设混进引擎包，其他行业接入时就会遇到"看似通用实际上只适配了第一个行业"的问题。
- **验证方法**：`registry/registry_test.go` 里的 `TestTwoUnrelatedDomainsCoexistWithoutEngineChanges` 是活文档——任何一次引擎改动之后，这个测试必须仍然只用两个词汇完全不重叠的领域（当前是 finance / security）就能验证通过，不需要为了让测试过而往引擎里加特殊分支。

### 1.5 核心模块零外部依赖，依赖只能住在卫星 module

核心 `cee` 模块（仓库根的 `go.mod`）**只允许依赖 Go 标准库**——`go.mod` 里必须没有任何 `require` 条目。这不是洁癖，而是这个项目可传播的卖点：任何人都能 `go build` 而不用信任、审计、拉取第三方代码。

需要重型后端（容器运行时、E2B/云沙盒 SDK、WASM 运行时、向量数据库客户端……）的实现,**不允许进核心**,必须放进 `satellites/<名字>/` 下、带自己独立的 `go.mod`。因为 `go build ./...` 不会进入带自己 `go.mod` 的子目录,卫星的依赖永远到不了核心。卫星必须通过核心已有的接口（`execution.Prober`、`llminjector.Extractor`、`intentrouter.Vectorizer` 等）插入,不得要求核心为它改动。

- **判据**：能用标准库 + 打 HTTP 端点解决的（如 `llmhttp`/`embedhttp` 打 OpenAI 兼容 API），留在核心；必须 vendor 某个 SDK 或 CGO/二进制运行时的，进卫星。
- **Code Review 检查点**：任何给根 `go.mod` 添加 `require` 的 PR，一律打回,请改成卫星 module。`satellites/dockersandbox` 是参考样板。

## 2. 命名规范

| 标识符 | 格式 | 示例 | 说明 |
|---|---|---|---|
| `DomainID` | 小写、单个单词或短横线 | `finance`、`network-security` | 全局唯一，两个领域不能同名 |
| `IntentNode.NodeID` | `<domain>.<snake_case 动作>` | `finance.duplicate_expense` | 域前缀避免跨域碰撞时难以定位来源 |
| `Workflow.WorkflowID` | `<domain>.<snake_case 流程名>` | `finance.flag_duplicate` | **同时也是 `IntentNode.EntryWorkflowRef` / manifest 里 `entry_workflow_ref` 要填的值**。这个字段以前叫 `EntryStepRef` / `entry_step_ref`，是个错名——它从来装的都是 workflow_id，不是 step_id。现已改名；旧的 JSON 名按第 3 条继续接受但会告警，**新 manifest 一律用 `entry_workflow_ref`** |
| `Step.StepID` | `<snake_case 动作>`，域内唯一即可,不需要域前缀 | `check`、`notify`、`human_review` | 只在所属 Workflow 内寻址,不需要跨域唯一 |
| `CircuitBreakerPolicy.PolicyID` | `<snake_case 策略意图>` | `escalate_to_review`、`security_containment_gate` | 命名应体现"失败后做什么",而不是"哪个 Step 用了它"——同一策略允许被多个 Step 引用 |
| `schema_ref` / `probe_ref` / `action_ref` | `<domain>.<snake_case 名称>` | `finance.expense_fields`、`finance.check_duplicate` | 与 `NodeID` 同样加域前缀,原因相同 |

## 3. Manifest 版本兼容规则

- `manifest.File` 新增字段时，必须使用 `omitempty` 或保证零值语义合理（现有 `StepSpec` 里的可选字段已按此处理），**不允许**让新增字段成为必填,否则历史 manifest 会直接解析失败而不是优雅降级。
- **不允许**删除或重命名 `manifest.File`/`IntentSpec`/`PolicySpec`/`WorkflowSpec`/`StepSpec` 里已存在的 JSON 字段名——这些是对外契约，改名等同于破坏所有已发布的 manifest。如确需废弃，先标记 deprecated 并保留字段至少一个大版本周期。
- `Load` 函数对任何无法识别的字段组合（未知 `type`、缺失的引用）必须返回 `error`，**不允许**静默忽略或使用默认值兜底——一个写错的 manifest 应该在加载时就失败，而不是在运行到一半时才暴露问题。

## 4. 贡献 PR 检查清单

任何修改以下内容的 PR，提交前自查：

- [ ] 是否触碰了 `entities`/`execution`/`intentrouter`/`llminjector`/`sandbox`/`registry`/`manifest`/`stdlib` 八个引擎包？如果是，`registry_test.go` 的双域测试是否仍然通过、且未新增行业特化分支（对应第 1.4 条）。
- [ ] 新增/修改 manifest 后，`go run ./cmd/cee validate <manifest.json>` 是否无 error。
- [ ] 新增标准动作时，动作名与参数里是否不含任何行业名词（对应第 1.4 条）；若它会产出判断性字段，是否符合第 1.1.1 条的豁免边界。
- [ ] 新增的 `Schema` 字段是否全部是事实性字段，没有决策字段混入（对应第 1.1 条）。
- [ ] 新增的 `Probe` 实现是否只读/模拟,审查者需要能一眼确认没有真实副作用（对应第 1.2 条）。
- [ ] 新增的 `CircuitBreakerPolicyRef` 使用是否指向了通过 `RegisterPolicy` 注册的策略,而不是在 `Action` 内部手写重试（对应第 1.3 条）。
- [ ] 新增的标识符（`NodeID`/`WorkflowID`/`PolicyID`/`*_ref`）是否遵循第 2 节命名规范。
- [ ] 是否为新路径补充了测试：至少一条成功路径 + 一条失败/未注册引用路径。
- [ ] `go build ./...`、`go vet ./...`、`go test ./...` 是否全部通过。
- [ ] 是否新引入了外部依赖？`go.mod` 目前零依赖是有意为之,新增依赖需要在 PR 描述里单独说明理由,不能顺带引入。

## 5. 错误处理规范

- 引擎包内部一律使用 Go 原生 `error`（含 `CircuitBreakerTripped` 这类自定义 error 类型），**不使用 `panic` 做正常业务流程控制**——`panic` 只允许用于真正的编程错误（例如内部不变量被破坏），且必须在包边界之前 `recover`。
- 需要判断具体错误类型时使用 `errors.As`/`errors.Is`（参考 `engine_test.go` 里对 `*CircuitBreakerTripped` 的断言方式），不使用字符串匹配错误信息。
