# 贡献指南 · Contributing to CEE

> English: [CONTRIBUTING.en.md](CONTRIBUTING.en.md)

欢迎贡献。CEE 的定位是一套"任何行业都能接进来"的开源协议，所以协作规则被刻意写死在文档里，而不是靠默契。这份指南告诉你**怎么开始**；具体的强制性红线在 [`docs/NORMATIVE_HANDBOOK.md`](docs/NORMATIVE_HANDBOOK.md)，动手细节在 [`docs/DEVELOPMENT_GUIDE.md`](docs/DEVELOPMENT_GUIDE.md)。

## 两类贡献

**A. 贡献一个领域插件**（最常见，也最欢迎）——你不需要改引擎，只需描述你的流程。

**B. 改进引擎本身**——`entities` / `execution` / `intentrouter` / `llminjector` / `sandbox` / `registry` / `manifest` / `stdlib` 这些跟业务无关的核心包。门槛更高，因为它们是所有插件共同依赖的契约。

## 开始之前

```bash
go build ./... && go vet ./... && go test ./...   # 三者必须全绿
```

仓库**零外部依赖**是有意为之——`go.mod` 目前没有任何 `require`。新增依赖需要在 PR 里单独说明理由，不能顺带引入。

## A. 贡献一个插件

### L1：无代码（纯 JSON）

如果你的流程能用标准动作库（`std.set` / `std.require` / `std.rule_check` / `std.suspend`）表达，就完全不用写 Go：

1. 在 `catalog/plugins/<你的插件名>/manifest.json` 写你的 manifest（结构参考 `catalog/plugins/sla-guard/manifest.json`）。
2. 用校验器自查——这是准入闸门，过不了别提 PR：
   ```bash
   go run ./cmd/cee validate catalog/plugins/<名字>/manifest.json
   ```
3. 在 `catalog/index.json` 加一条 entry（`name` / `tier: "L1"` / `version` / `domain` / `manifest` 路径）。
4. 整体校验 catalog：
   ```bash
   go run ./cmd/cee lint      # 必须 ok: no issues
   ```
5.（可选，但推荐）加一份 `benchmark.json` 标准事件集并在 entry 里加 `benchmark` 字段，让你的插件上排行榜：
   ```bash
   go run ./cmd/cee bench
   ```

分支怎么写：引擎没有 if/else，用 `std.require`——条件成立走 `on_success`，不成立则失败、经 `circuit_breaker_policy_ref` 路由到 fallback step。要"挂起等人工/回调"用 `std.suspend`。

### L2：有代码（manifest + Go Hooks）

标准动作表达不了的逻辑（比如要碰外部系统），把 `action_ref` 指向一个具名 Go 函数（`manifest.Hooks`）。L2 插件走 Go module 分发，不通过 catalog 的 `install`，但可以在 index 里用 `tier: "L2"` 登记以便被发现。写法见开发文档第 3 节。

## B. 改进引擎

改核心包前，先读 [`docs/NORMATIVE_HANDBOOK.md`](docs/NORMATIVE_HANDBOOK.md) 第 1 节的四条架构红线。**违反即拒绝合并**，概括：

1. **LLM 只能抽取，不能决策**——`Schema` 里不允许出现 `is_fraud`/`should_alert` 这类判断性字段。
2. **沙盒探针只读**——不允许有任何真实副作用。
3. **断路器走命名策略**——不允许在 Action 里手写重试循环。
4. **引擎包不含行业逻辑**——不允许出现 `if domainID == "finance"` 这类分支；`stdlib` 的动作名和参数里不能有行业名词。

守护测试：`registry/registry_test.go` 的 `TestTwoUnrelatedDomainsCoexistWithoutEngineChanges` 用两个词汇完全不重叠的领域验证"引擎不含行业逻辑"。你的改动之后它必须仍然通过，且你没有为了让它过而往引擎里加特殊分支。

## PR 检查清单

提交前逐条自查（完整版在规范手册第 4 节）：

- [ ] `go build ./...` / `go vet ./...` / `go test ./...` 全绿，`gofmt` 干净。
- [ ] 新增插件：`cee validate` 通过；进 catalog 的话 `cee lint` 干净。
- [ ] 碰了引擎核心包：四条红线都守住，双域守护测试仍过。
- [ ] 新路径补了测试：至少一条成功路径 + 一条失败/未注册引用路径。
- [ ] 命名遵循规范手册第 2 节（`NodeID`/`WorkflowID`/`PolicyID`/`*_ref` 的域前缀约定）。
- [ ] 没有顺带引入外部依赖（如需引入，PR 描述里单独说明）。

## 代码风格

- Go 官方风格，提交前跑 `gofmt -w`。
- 测试用标准库 `testing` + `errors.As`/`errors.Is`，不引第三方断言库。
- 错误处理用原生 `error`；`panic` 只用于真正的编程错误。

## 行为准则

对人友善，对事严格。评审针对代码不针对人；分歧用测试和数据说话。

## License

提交贡献即表示你同意以本项目的 [Apache License 2.0](LICENSE) 授权你的贡献。
