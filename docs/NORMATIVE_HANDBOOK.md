# CEE 规范性开发手册

本手册是强制性规则，不是建议。区别在于：`DEVELOPMENT_GUIDE.md` 教你怎么做，本手册规定什么**不允许**做，以及为什么——因为 CEE 定位是任何行业都能接入的开源协议，规则必须写死在文档里被所有贡献者共同遵守，而不能靠"大家都懂行业惯例"这种默契。

每条规则标注了违反后果，供 Code Review 时引用。

## 1. 架构红线（违反即拒绝合并）

### 1.1 LLM 只能抽取，不能决策

`llminjector.Extractor` 的返回值只允许包含"从原文里抽出来的事实性字段"（金额、日期、实体名称……），**不允许包含任何"下一步该怎么办"性质的字段**（是否异常、是否放行、严重等级……）。

- **为什么**：这是 CEE 区别于"LLM 全权 Agent"的核心边界。一旦决策权混进抽取结果，确定性执行引擎就退化成了"LLM 说了算，代码只是壳子"。
- **强制机制**：`Injector.Extract` 只拷贝 `Schema` 里声明过的字段到 `StructuredPayload`，schema 之外的字段会被静默丢弃（见 `injector.go` 的 `clean` 构造逻辑）。**因此这条规则实际上是靠"不要在 Schema 里声明决策字段"来落地的**——Code Review 时如果看到一个 `Schema` 里出现 `is_valid`、`should_alert`、`severity_level` 这类字段名，必须打回。
- 合法字段名的形状：名词性、可从原文直接验证真伪（`amount`、`merchant_name`、`invoice_id`）。非法字段名的形状：判断性、需要业务规则才能得出结论（`is_fraud`、`risk_score`、`action`）。

### 1.2 Sandbox Probe 必须只读/模拟，不能有真实副作用

`sandbox.Probe` 注册的函数运行在"预演"阶段，**在真实 Step 执行之前**。探针内部不允许调用任何会修改外部状态的 API（转账、发通知、改配置、写数据库……）。

- **为什么**：沙盒存在的意义是"先模拟再决定要不要真做"；如果探针本身有副作用，"预演"这个概念就不成立了，等于执行了两次。
- **Code Review 检查点**：任何 Probe 函数体内出现 `POST`/`PUT`/`DELETE` 语义的调用，或任何写数据库操作，直接打回。允许的操作：只读 API 调用、连通性检查、dry-run 模式的 API（前提是该 API 的 dry-run 参数被第三方证实不产生副作用）。

### 1.3 断路器策略必须以命名引用声明，不允许内联

`LeafStep`/`CompositeStep` 的 `CircuitBreakerPolicyRef` 必须指向一个通过 `Engine.RegisterPolicy` 注册的策略。**不允许**在 `Action` 函数内部手写 `for`/重试循环、`time.Sleep` 退避、或者硬编码的 fallback 逻辑来绕开这个机制。

- **为什么**：策略集中注册是"全局可审计"的前提——治理者需要能回答"系统里一共有多少条安全网、分别兜底到哪"，如果重试逻辑散落在各个 `Action` 函数体内部，这个问题永远答不出来。
- **Code Review 检查点**：`Action`/`Run` 函数体内出现循环 + `time.Sleep`/重试计数器组合模式，视为违规,应该重构成"失败就返回 error，由 `CircuitBreakerPolicyRef` 接管"。

### 1.4 引擎包本身不允许出现行业逻辑

`entities`、`execution`、`intentrouter`、`llminjector`、`sandbox`、`registry`、`manifest` 这七个包，**任何时候都不允许 import 或硬编码某个具体行业的概念**（不能出现 `if domainID == "finance"` 这类分支，不能有字段叫 `InvoiceAmount` 而不是通用的 `map[string]any`）。

- **为什么**：这是"引擎只认引用不认内容"的字面意义。一旦某个行业的假设混进引擎包，其他行业接入时就会遇到"看似通用实际上只适配了第一个行业"的问题。
- **验证方法**：`registry/registry_test.go` 里的 `TestTwoUnrelatedDomainsCoexistWithoutEngineChanges` 是活文档——任何一次引擎改动之后，这个测试必须仍然只用两个词汇完全不重叠的领域（当前是 finance / security）就能验证通过，不需要为了让测试过而往引擎里加特殊分支。

## 2. 命名规范

| 标识符 | 格式 | 示例 | 说明 |
|---|---|---|---|
| `DomainID` | 小写、单个单词或短横线 | `finance`、`network-security` | 全局唯一，两个领域不能同名 |
| `IntentNode.NodeID` | `<domain>.<snake_case 动作>` | `finance.duplicate_expense` | 域前缀避免跨域碰撞时难以定位来源 |
| `Workflow.WorkflowID` | `<domain>.<snake_case 流程名>` | `finance.flag_duplicate` | **同时也是 `IntentNode.EntryStepRef` 要填的值**——这是当前实现里一个容易踩坑的耦合点：`EntryStepRef` 语义上是"入口工作流"，不是"入口步骤"，命名沿用是历史遗留，新增代码时按此约定填写,不要按字面意思去找一个"Step" |
| `Step.StepID` | `<snake_case 动作>`，域内唯一即可,不需要域前缀 | `check`、`notify`、`human_review` | 只在所属 Workflow 内寻址,不需要跨域唯一 |
| `CircuitBreakerPolicy.PolicyID` | `<snake_case 策略意图>` | `escalate_to_review`、`security_containment_gate` | 命名应体现"失败后做什么",而不是"哪个 Step 用了它"——同一策略允许被多个 Step 引用 |
| `schema_ref` / `probe_ref` / `action_ref` | `<domain>.<snake_case 名称>` | `finance.expense_fields`、`finance.check_duplicate` | 与 `NodeID` 同样加域前缀,原因相同 |

## 3. Manifest 版本兼容规则

- `manifest.File` 新增字段时，必须使用 `omitempty` 或保证零值语义合理（现有 `StepSpec` 里的可选字段已按此处理），**不允许**让新增字段成为必填,否则历史 manifest 会直接解析失败而不是优雅降级。
- **不允许**删除或重命名 `manifest.File`/`IntentSpec`/`PolicySpec`/`WorkflowSpec`/`StepSpec` 里已存在的 JSON 字段名——这些是对外契约，改名等同于破坏所有已发布的 manifest。如确需废弃，先标记 deprecated 并保留字段至少一个大版本周期。
- `Load` 函数对任何无法识别的字段组合（未知 `type`、缺失的引用）必须返回 `error`，**不允许**静默忽略或使用默认值兜底——一个写错的 manifest 应该在加载时就失败，而不是在运行到一半时才暴露问题。

## 4. 贡献 PR 检查清单

任何修改以下内容的 PR，提交前自查：

- [ ] 是否触碰了 `entities`/`execution`/`intentrouter`/`llminjector`/`sandbox`/`registry`/`manifest` 七个引擎包？如果是，`registry_test.go` 的双域测试是否仍然通过、且未新增行业特化分支（对应第 1.4 条）。
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
