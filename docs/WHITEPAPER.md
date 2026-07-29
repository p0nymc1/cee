# 认知执行引擎（CEE）技术白皮书

> 重塑智能体执行范式，从"概率试错"到"确定性工程"

**本文档与代码同仓库、同版本管理。** 所有数字均可由 `go run ./cmd/cee bench` 或 CI 复现；所有"已建成"的说法都附有代码或测试链接。凡是尚未实测的指标，一律标注为**目标值**而不是成果——一份在立项时经不起追问的白皮书，比没有白皮书更糟。

实时实证：**https://p0nymc1.github.io/cee/** （由 CI 每小时在干净机器上重跑生成，非手写）

---

## 摘要

当前基于大语言模型的智能体，在长周期、高并发、成本敏感的企业场景中遭遇显著瓶颈。CEE（Cognitive Execution Engine）摒弃"依赖 LLM 全程决策"的范式，提出**意图确定性路由 + 边缘 LLM 注入 + 环境预演沙盒**的分层架构，把执行主控权交还给确定性状态机，LLM 降级为只做抽取、不做决策的边缘工具。

与常见做法不同，CEE 不是把 Temporal、向量数据库、云沙盒粘在一起，而是**先把协议本身定死，再让每一层的后端可替换**。当前内核**零外部依赖**（`go.mod` 无任何 `require`），10691 行 Go，21 个包，198 个测试，`go build` 在断网环境下可完成。

---

## 第一章 背景与行业痛点

### 1.1 当前 Agent 的四个硬伤

主流 Agent 工程（Prompt → Context → Harness → Loop → Self-Harness → Environment）本质是"给大模型穿上外衣"：

1. **成本黑洞**：简单任务也要消耗大量 Token 做思维链推理。
2. **执行失控**：LLM 的随机性使系统成为黑盒，易陷死循环。
3. **记忆脆弱**：上下文窗口有物理上限，撑不起跨周期的复杂任务。
4. **修复滞后**：依赖失败后的自我修正，容错成本极高。

### 1.2 一个常被忽略的第五点

上述四点是行业共识。但真正阻挡企业开启自动化的，往往是第五点：

> **正确的判断，仍然可以有灾难性的执行。**

安全团队不敢开自动处置，不是怕检测器误报，而是怕它**报对了**——封锁密码喷洒的来源 IP，而那个 IP 是自家 VPN 出口，900 名远程员工当场断线。这类事故与模型智商无关，再聪明的模型也阻止不了。

CEE 把这一点当作一等公民：**执行前预演，而不是执行后反思**。

---

## 第二章 核心架构

四层逻辑，与代码一一对应：

| 层 | 代码 | 职责 |
|---|---|---|
| 意图路由层 | [`intentrouter`](../intentrouter/router.go) | 自然语言 → 预注册意图，按域隔离，不跨域检索 |
| 确定性执行引擎（DEE） | [`execution`](../execution/engine.go) | 走 Step DAG，沙盒门禁、断路器、挂起/恢复、补偿 |
| 边缘 LLM 注入器 | [`llminjector`](../llminjector/injector.go) | 唯一允许 LLM 运行的地方，且只做文本→字段抽取 |
| 预执行沙盒 | [`sandbox`](../sandbox/sandbox.go) | 有副作用的 Step 执行前先模拟一次 |

### 2.1 技术选型：先定协议，再选后端

白皮书类文档常见的写法是罗列技术栈。CEE 的选择相反——**内核不绑定任何后端**，每一层暴露一个接口，重型实现放进独立的卫星模块：

| 接口 | 内核默认实现 | 可替换为 |
|---|---|---|
| `intentrouter.Vectorizer` | 词汇 Jaccard（零依赖） | 任意 OpenAI 兼容 embedding 端点（[`embedhttp`](../embedhttp/embedhttp.go)）、向量数据库 |
| `llminjector.Extractor` | 领域自注册函数 | 任意 OpenAI 兼容 chat 端点（[`llmhttp`](../llmhttp/llmhttp.go)） |
| `execution.Prober` | 进程内调用 | 容器（[`satellites/dockersandbox`](../satellites/dockersandbox)）、WASM（[`satellites/wasmhooks`](../satellites/wasmhooks)）、E2B 等 |
| `execution.Store` | 内存 | 落盘（[`filestore`](../filestore/filestore.go)）、数据库 |

**规范手册 1.5 条**规定：核心模块只允许依赖标准库，任何需要重型 SDK 的实现必须放进带独立 `go.mod` 的卫星模块。`go build ./...` 不会下降到卫星目录，因此卫星的依赖**在物理上到不了核心**。

这带来的实际好处：任何人可以 `go get` 之后立刻 `go build`，无需信任、审计、拉取任何第三方代码——对企业安全审计而言，这比"我们集成了业界最佳组件"有力得多。

### 2.2 引擎只认引用，不认内容

四个组件之间只交换 [`entities`](../entities/entities.go) 定义的七个固定形状，从不传递未经约定的 map。因此任何行业的业务逻辑都能作为领域插件接入，引擎代码一行不改。

这条由一个"活文档"测试守着：`registry_test.go` 的双域测试要求任何引擎改动之后，两个词汇完全不重叠的领域仍能共存，且不得为通过测试而往引擎里加行业分支。

---

## 第三章 突破点

### 3.0 定位：编译器，不是解释器

CEE 与 Agent 的根本差别不在"用不用模型"，而在**模型什么时候工作**。

Agent 在**运行时**推理：同一件事执行一万次，就推理一万次；而且它的计划只在执行过程中存在，事前无从审查。

CEE 在**设计时**推理一次。模型的产出不是动作，而是一份 manifest——可读、可静态检查、可拿历史数据重放、可由人批准。之后流程确定性执行，零模型成本，且周二不会给出跟周一不同的答案。

> **Agent 是解释器，CEE 是编译器。**

这不是让模型走开——把一段混乱的人话变成一份正确的流程，比照着流程执行难得多，那才是模型该做的事。它只是不该做一万遍。

`cee draft`（见 3.7）实现了这条链路。

### 3.1 决策范式：状态机驱动

常规路径完全由代码定义，LLM 不参与流程决策。引擎在类型系统层面封闭了 Step 形态——`Step` 接口有一个未导出方法，**只有本包的 `LeafStep` 和 `CompositeStep` 能满足它**，不存在第三种。

断路器策略必须**具名注册**，禁止在动作内部手写重试循环，因此"系统里一共有多少条安全网、分别兜底到哪"可以从策略表一处审计。

### 3.2 事前推演，而非事后反思

沙盒探针在真实动作**之前**运行，只读。探针拒绝则动作根本不执行，经断路器转人工，并把拒绝原因带过去。

真实运行的输出（取自实证页）：

```
ALERT  password spray against the vpn gateway
       peer=203.0.113.11 host=vpn-gw01 confidence=0.97
       (the 'attacker' address is our own VPN egress)
  -> matched network-detection.T1110_password_spray (1.00)
  -> held for an analyst: containment would have hit something it must not
     because: 203.0.113.11 is our VPN concentrator — about 900 remote workers egress here — blocking it hits us, not the attacker
     trace: [require_confidence select_response contain hold_for_analyst]
```

### 3.3 等待不是失败：跨周期状态持久化

流程需要等外部事件（人工审批、维护窗口、回调）时，动作返回 `*Suspended`。引擎存档并返回一个 `crypto/rand` 生成的恢复指针，**正常返回**——挂起不是错误，断路器看不到它。

`Engine.Resume(pointer, resolution)` 把外部决定合并进存档的 context，从挂起点的下一步继续。配合 [`filestore`](../filestore/filestore.go) 可跨进程重启存活。

三条安全保证：指针一次性（`Consume` 原子抢占，审批不可重放）、取用两阶段（`Release` 之前 claim 一直保留，崩溃的运行可被 `Orphaned()` 发现而不是凭空消失）、未配 Store 时挂起**报错而非静默降级**。

### 3.4 抽取是猜测，不是事实

这是对"LLM 只能抽取不能决策"的补强。裁掉决策字段挡不住**抽错一个数**——把 $50,000 读成 $5,000 的抽取器什么都没决定，却什么都决定了。

因此 `Injector.Extract` **结构性地**给它产出的每个字段打上来源标记（`ExtractionResult.ModelDerived`），抽取器无法豁免。有后果的步骤用 `std.require_verified` 声明哪些字段不接受猜测值。

刻意不使用置信度分数：**模型自报的置信度引擎无法审计，而一个没人能核实的数字比没有数字更糟——它制造虚假的安心**。

### 3.5 补偿：撤销已经发生的副作用

探针管"别做那个动作"，但对"第 4 步失败时前三步已经转了账"无话可说。`LeafStep` 可声明 `compensate_with`，运行被放弃时**逆序**撤销。

补偿失败绝不重试、绝不吞掉，会以 `COULD NOT UNDO` 出现在错误里——动作发生了、撤销也失败了，世界处在一个没人选择的状态，这是引擎能报告的最坏情况。

### 3.6 确定性的兑现：重放与回归 diff

这是 CEE **唯一别人给不了**的能力。确定性的独有红利是"同样输入必然同样输出"，而它只有被兑现才有价值：

```
把限额从 $100 收紧到 $50，重放一笔已经放行的 $80 退款：
  trace:       pay  ->  pay -> pay -> hold
  output.paid: true ->  false
```

**改一条规则，算出上季度哪些决定会翻转。** Temporal 有持久化重试、Airflow 有调度，但这件事只有确定性引擎做得到。

实现上不需要改引擎：探针和抽取是非确定性唯一的入口，[`replay`](../replay/replay.go) 通过包装 `Prober` 和 `llminjector.ResultObserver` 录制并回放它们。一个副作用是它能抓出违反确定性契约的动作——规则和裁决都没变，结果却飘了。

### 3.7 模型起草工作流（`cee draft`）

```bash
cee draft "报销超过一万要经理批，账户已关闭的一律不放款"
```

模型在一个**封闭动作词表**（`std.*` 加已注册 hook）内组合出 manifest，随后经过四道闸门：

1. **manifest 只描述形状，永远夹带不了行为**——模型最坏能产出的，是一个已存在、已审查过动作的错误排列。
2. **编造的动作过不去**——`Validate` 告警、`Load` 拒绝绑定标准库和 hook 里都没有的名字。
3. **仅有警告也不放行**——`Validate` 看不到领域 Go hook，因此声称不需要 hook 的草稿会额外 `Load` 一次，把"只有警告"变成真保证。
4. **纠错循环有硬上限**——把校验错误喂回去让模型改是有用的，但**无边界的纠错循环恰恰是本项目要消灭的失败模式**，`MaxAttempts` 是硬顶，始终不通过则如实报告而非返回。

CLI 只打印、不存盘：草稿是提案，把模型挪到设计时的全部意义就是让人先读一遍。

这一步同时消除了 CEE 原本最实在的接入成本——**不再要求使用者先自己把流程理清楚**。

---

## 第四章 现状与路线图

### 4.1 已完成（有代码与测试）

| 能力 | 位置 |
|---|---|
| 四层核心架构 | `intentrouter` / `execution` / `llminjector` / `sandbox` |
| 领域插件注册 + JSON manifest 声明式加载 | `registry` / `manifest` |
| 静态校验器（含环检测、可达性、悬空引用） | `manifest.Validate`、`cee validate` |
| 无代码（L1）标准动作库 | `stdlib`（`std.set`/`require`/`rule_check`/`suspend`/`require_verified`）|
| 挂起/恢复 + 落盘 Store | `execution` + `filestore` |
| 补偿（saga） | `execution/compensate.go` |
| 恢复授权（audience + Authorizer） | `execution/authorize.go` |
| 来源标记与验证门禁 | `entities.ModelDerived` + `std.require_verified` |
| 重放与回归 diff | `replay` |
| 模型起草工作流 | `draft` / `cee draft` |
| 度量与基准排行榜 | `scorecard` / `bench` / `cee bench` |
| 社区插件目录 | `catalog` / `cee list`·`lint`·`install` |
| 真实模型后端（零依赖） | `llmhttp` / `embedhttp` |
| 卫星模块样板 | `satellites/dockersandbox`、`satellites/wasmhooks` |

六类元场景均已落地为可运行示例：安全监测、审批流、工单路由、调度、数据同步、异常监控（另含网络入侵检测与虚拟货币市场监控两个真实案例）。

### 4.2 尚未完成（按优先级）

1. **服务形态**：CEE 目前是库，不是服务。要 HTTP API 需自行封装。
2. **服务层的身份认证**：引擎侧的授权已就位（挂起可声明 audience，由领域 `Authorizer` 裁决，默认拒绝，拒绝不消耗指针，批准者记入 `cee.resumed_by`），但**证明"调用方是谁"属于引擎前面那层服务**，那层还不存在。
3. **起草链路的真实模型验证**：`draft` 的逻辑与四道闸门已全部测试（用桩，不联网），但"模型第一次能不能写对"尚未用真实端点实测。
4. **可视化编排**：`cee draft` 已提供自然语言入口，拖拽界面仍缺；底层是纯 JSON 且可静态校验，前端可后补。
5. **并行与汇合**：引擎目前无 fan-out/fan-in 原语。
6. **挂起超时与升级**：等待没有尽头，缺 TTL 与超时转派。
7. **诊断性度量**：`scorecard` 量的是产出（确定性步数、消除的调用数），未量误差（意图未命中率、探针拒绝率、转人工比例）。**只量好看的数字是一种概念偏向。**

---

## 第五章 度量口径

### 5.1 实测数字

`cee bench` 当前输出（可复现）：

```
rank plugin           determinism  events     errors   LLM calls eliminated vs agent
1    access-review    100%         4          0        8 of 8
2    sla-guard        100%         4          0        8 of 8
```

口径：以"每步调用一次模型的朴素 Agent"为基线，统计 CEE 消除了多少次调用。**不做 token 估算**——估算不可验证，写进汇报材料会在第一个追问处崩掉。

### 5.2 目标值（尚未实测，不得作为成果引用）

- 意图路由命中率 > 85%
- 常规路径成本相对基线 Agent 下降 > 80%
- 响应延迟进入毫秒级

这三项需要真实业务流量与对照实验才能得出。当前代码**没有度量意图命中率的机制**（见 4.2 第 6 条），这是取得该数字前必须先补的。

---

## 第六章 落地场景

| 场景 | CEE 的着力点 | 已有实证 |
|---|---|---|
| 网络安全响应 | 处置动作的爆炸半径护栏 | [`examples/network_detection`](../examples/network_detection) |
| 审批与风控 | 挂起/恢复跨周期、验证门禁 | [`examples/human_approval`](../examples/human_approval) |
| 跨系统数据同步 | 写入前冲突预演、逐条独立处置 | `examples/meta_scenarios` |
| 工单路由 | 两条出边表达 N 路分支 | `examples/manifests/ticket-routing.json` |
| 变更调度 | 无时钟引擎上的窗口延后 | `examples/manifests/change-window.json` |
| 市场异常监控 | 数据新鲜度与流动性门槛 | [`examples/crypto_surveillance`](../examples/crypto_surveillance) |

### 适用边界（同样重要）

- 流程真正需要开放式推理时**不适用**——CEE 的整套设计就是不让模型做决定。
- 仅需定时任务是 cron 的领域；需要分布式持久化编排与重试语义是 Temporal 的领域。
- CEE 的差异化只在一处：**有不可逆操作、且需要审计与回溯的确定性流程**。

---

## 第七章 与开源生态的关系

CEE 不重新发明组件，也不预先绑定组件。它定义**连接协议与分层边界**，把后端选择留给接入方：

- 需要分布式编排 → 卫星模块封装 Temporal，核心不变
- 需要向量检索 → 实现 `Vectorizer`，接 Qdrant 或任何 embedding 服务
- 需要强隔离沙盒 → 实现 `Prober`，接 E2B、容器或 WASM

**核心永远零依赖**是一条硬规则而非阶段性状态（规范手册 1.5）。任何向根 `go.mod` 添加 `require` 的 PR 一律打回。

---

## 第八章 总结

在模型能力逐渐趋同的当下，护城河不再是 Prompt 写得好，而是**谁能用更低成本、更高确定性，把 AI 能力规模化塞进企业核心业务系统**。

CEE 不追求模型的全知全能，追求执行层面的可靠与可审计。它的三条主张，每一条都有代码和测试而非修辞支撑：

1. **决策归代码**——LLM 只抽取，且抽取结果带来源标记，猜测不能冒充事实。
2. **危险动作先预演**——执行前只读探针，拒绝则转人工并说明原因；已发生的副作用可逆序补偿。
3. **确定性要能兑现**——改规则之前，先算出它会改变哪些已经发生的决定。

---

*本文档随代码演进。发现与实现不符之处，以代码为准，并请提 issue 或直接修正本文档。*
