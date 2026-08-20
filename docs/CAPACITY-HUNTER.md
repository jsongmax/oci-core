# 容量守候（抢机）设计文档

> 状态：**已实现**。撰写于 2026-08-19（提交 `d8a3bb4`），实现见
> `internal/store/hunt.go`、`internal/huntsvc/`、`internal/httpapi/hunt.go`、
> `web/src/views/HuntView.vue`。
>
> 与本文的差异：
> - §3 的表结构多了 `name` 与 `interval_seconds` 两列（任务需要一个人能认的名字，
>   间隔改成每任务可配而不是全局常量）。
> - §4 的间隔从"固定 60 秒基准"改成"用户可配、硬下限 30 秒"，超出建议值时
>   表单与 L2 确认框都会给出限流风险警告。
> - §10 的四个阶段一次做完，§6 的防重复创建随第一版一起上线（本来就不该延后）。

---

## 1. 这是什么,不是什么

**是**:一个持久化的后台任务——反复尝试创建某个规格的实例,直到成功或被叫停。
针对的是 Oracle 免费额度里 ARM(`VM.Standard.A1.Flex`)长期没有容量这一现实:
`LaunchInstance` 大概率返回"该可用域暂时没有此规格的宿主机",需要在容量释放的
那个瞬间恰好发出请求。

**不是**:一个"薅羊毛脚本"。它不注册账号、不绕过任何限制、不隐藏自己的来源。
它做的事和你在 Oracle 控制台点"创建实例"完全一样,只是替你重试。

### 与项目原定范围的关系

项目开局把范围定成"账号管理 + 实例管理,抢机明确不做"。加这个功能会改变工具的
性质:从**管理**变成**带自动化的获取**。这个改变的代价写在 §9,不在技术上,
在账号风险上。**这是一个产品决策,不是技术决策**,应当先决定要不要,再谈怎么做。

---

## 2. 已有的地基

比预想的多。真正缺的只有"任务"这一层。

| 能力 | 位置 | 状态 |
|---|---|---|
| `LaunchInstance` | `internal/ociclient/compute.go:324` | 完整,含 `ShapeConfig` / `SourceDetails` / `CreateVnicDetails` / `Metadata` |
| 容量不足的识别 | `internal/ociclient/errors.go` | `ClassOutOfCapacity`,`minBackoff: 30s`,`retryable: true` |
| 限流识别与退避 | 同上 | `ClassThrottled`,`minBackoff: 5m`,且尊重响应头 `Retry-After` |
| 配额耗尽识别 | 同上 | `ClassQuotaExceeded`,`retryable: false`——重试无意义,必须停 |
| 全局并发限流 | `internal/ociclient/limiter.go` | 租户 6 / 全局 16,新任务自动受约束 |
| 可用域枚举 | `ListAvailabilityDomains` | 有 |
| 自动建网 | `internal/netsvc` | 有,创建实例已在用 |
| 通知推送 | `internal/notify` | 五种渠道 + 事件订阅矩阵 |
| 免费额度常量 | `internal/ociclient/freetier.go` | ARM 2 OCPU / 12 GB(2026-06-15 起) |

`errors.go` 里那句注释说明这条路早就被考虑过了:

> 500 需要额外看 message,因为 Oracle 把"容量不足"塞进了通用的 InternalError 里。

**缺的**:一张任务表、一个能在重启后续跑的调度循环、一套 AD/FD 轮换、
一个防重复创建的护栏、以及对应的 UI。估计 600–900 行。

---

## 3. 数据模型

```sql
CREATE TABLE hunt_tasks (
    id           TEXT PRIMARY KEY,
    account_id   TEXT NOT NULL,
    region       TEXT NOT NULL,

    -- 启动参数快照。存 JSON 而不是拆成列：
    -- LaunchInstanceRequest 有十几个字段且会随 OCI 演进，
    -- 拆列意味着每加一个参数就要迁移一次表。
    spec         TEXT NOT NULL,

    -- 轮换范围。空表示"该区域全部可用域"。
    ads          TEXT NOT NULL DEFAULT '',

    -- pending / running / succeeded / failed / paused
    state        TEXT NOT NULL DEFAULT 'pending',

    attempts     INTEGER NOT NULL DEFAULT 0,
    -- 最近一次尝试的分类与原文，UI 上要显示“为什么还没成”
    last_class   TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    last_ad      TEXT NOT NULL DEFAULT '',

    -- 下次可以发起尝试的时间。调度器只看这一个字段决定该不该动手。
    next_at      INTEGER NOT NULL DEFAULT 0,

    -- 成功后落地的实例 OCID
    instance_id  TEXT NOT NULL DEFAULT '',

    -- 硬性上限，防止任务永远跑下去
    max_attempts INTEGER NOT NULL DEFAULT 0,   -- 0 = 不限
    expires_at   INTEGER NOT NULL DEFAULT 0,   -- 0 = 不过期

    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX idx_hunt_due ON hunt_tasks(state, next_at);
```

**为什么 `next_at` 落库而不是只放内存**:进程重启后必须原样恢复退避进度。
只存内存的话,每次重启都从零开始——用户重启一次容器,退避就被清空,
等于变相提高了请求频率,而这正是最不该发生的事。

---

## 4. 调度器

### 结构

单个 goroutine,10 秒一跳,每跳做三件事:

1. `SELECT ... WHERE state='running' AND next_at <= now ORDER BY next_at LIMIT N`
2. 对取出的每个任务发起**一次**尝试(并发受 `ociclient` 现有限流器约束)
3. 按结果写回 `next_at` / `attempts` / `last_*`

**刻意不用"每个任务一个 goroutine + sleep"**:任务数一多就是几十个睡眠协程,
而且退避状态只活在栈上,重启即失。`next_at` + 统一轮询是可恢复的。

`N` 的上限建议 4:同一时刻最多 4 个账号在发 `LaunchInstance`。这不是性能限制,
是风控限制——见 §9。

### 状态机

```
pending ──启动──> running ──成功──> succeeded
                     │
                     ├──不可重试的错误──> failed
                     ├──用户暂停──────> paused ──恢复──> running
                     └──超次数/超时────> failed
```

### 错误分类 → 动作

直接复用 `ociclient` 现有的 `Class`,不新造一套:

| Class | 动作 | 下次间隔 |
|---|---|---|
| `ClassOutOfCapacity` | 换下一个 AD,继续 | 基准间隔(见下) |
| `ClassThrottled` | **全账号**降速,不只这个任务 | `max(Retry-After, 5min)` × 退避系数 |
| `ClassQuotaExceeded` | 立即 `failed`,推通知 | — |
| `ClassAuthFailed` / `ClassNotAuthorized` | 立即 `failed`,并标记账号异常 | — |
| `ClassBadRequest` | 立即 `failed`,原样回显参数错误 | — |
| `ClassTransient` | 原 AD 重试 | 30s,指数退避封顶 5min |
| 成功 | `succeeded`,写 `instance_id`,推通知 | — |

**`ClassThrottled` 必须降的是整个账号而不是单个任务**。429 是账号级信号,
只把当前任务退避、其他任务照跑,等于没退。实现上:在账号维度维护一个
`throttledUntil`,调度器取任务时跳过这个账号的全部任务。

### 间隔

这是整个设计里最需要克制的一个数字。

- **基准 60 秒**,每个任务独立计时
- 连续 `OutOfCapacity` 达到 10 次后,基准翻倍到 120 秒;30 次后 300 秒
- 抖动 ±20%,避免多个任务在整分钟同时开火
- 硬下限 30 秒,配置项不允许低于此值

为什么不是"越快越好":Oracle 的容量释放不是毫秒级事件,一台机器被释放后
通常在数分钟内可被申领。把间隔从 60 秒压到 5 秒,命中率提升有限,而请求量
是 12 倍——收益与风险严重不对称。

---

## 5. 可用域与故障域轮换

容量在 AD 之间不均衡,只盯一个 AD 会显著降低命中率。

- 任务创建时若未指定 AD,取该区域全部 AD(`ListAvailabilityDomains`)
- 每次尝试按顺序取下一个,失败即前进——**不在同一个 AD 上连续撞**
- 故障域(`faultDomain`)**留空**。指定 FD 只会缩小可调度范围,
  除非用户明确要求分散部署,否则交给 Oracle 自己挑

**跨区域不做**。跨区域抢机意味着同时向多个 region 发请求,请求总量成倍上升,
而且免费额度本身是账号级的,抢到别的区域也未必是用户想要的。要换区域,
让用户再建一个任务——显式优于隐式。

---

## 6. 防重复创建(必须做)

**这是最容易被忽略、后果最实在的一个点。**

`LaunchInstance` 是非幂等的。如果请求实际到达了 OCI 并成功,但响应在网络上丢了,
客户端看到的是超时;此时按 `ClassTransient` 重试,就会创建出**第二台**实例。
对 ARM 免费额度而言,第二台会直接吃掉剩余配额,而用户毫不知情。

三层防护:

1. **打标**。每次 `LaunchInstance` 带 `freeformTags: { "ocicore-hunt": <task_id> }`。
2. **重试前先查**。任何一次重试之前,先用该标签 `ListInstances`;
   已经存在非 TERMINATED 的实例就直接判定为成功,不再发起创建。
3. **超时归类为"未知"而非"可重试"**。请求超时时状态是不确定的,
   走第 2 步确认过再决定,不能默认重发。

第 2 步会让每次重试多一个只读请求。值得——一次误创建的代价远大于此。

---

## 7. 与免费额度的关系

创建任务时按 `freetier.go` 的常量做**预检并提示**,但不阻断:

- ARM:`AlwaysFreeARMOcpus = 2` / `AlwaysFreeARMMemoryGB = 12`
- Micro:`AlwaysFreeMicroInstances = 2`
- 块存储:`AlwaysFreeBlockGB = 200`

预检要把**已有实例的占用**算进去。用户已经有一台 1 OCPU 的 A1,再抢 2 OCPU
就会超。超了不是拒绝,是明确告知"这会超出免费额度,超出部分按量计费"——
升级账号本来就可以合法超额,替用户做决定是越权。

注意 2026-06-15 的政策变化:ARM 免费额度从 4/24 降到 2/12,超限实例从
2026-08-18 起会被回收。**默认预设必须是 2/12**,给 4/24 等于把用户的机器
送进回收队列。

---

## 8. UI

新增一个"容量守候"页,或并入实例页的一个标签。每个任务一行:

```
[● 运行中]  A1.Flex 2C/12G · 圣何塞 FREE · us-sanjose-1
            已尝试 247 次 · 当前 AD-2 · 上次 03:14 容量不足
            下次尝试 00:47 后                      [暂停] [删除]
```

必须显示的三件事:

- **`attempts` 与 `last_error`**。没有这两个,用户面对的是一个黑盒,
  只能反复问"到底在不在跑"
- **`next_at` 倒计时**。让退避策略可见——否则用户会觉得"卡住了"而去点重启
- **当前轮到哪个 AD**。轮换是这个功能的核心机制,藏起来没有好处

创建表单直接复用现有的"创建实例"抽屉,只是提交目标不同:一个立即创建,
一个建任务。参数完全一致,不要做两套表单。

---

## 9. 风险(必须让用户知情)

**这一节的内容应当原样出现在 UI 上,不是只写在文档里。**

高频调用 `LaunchInstance` 是 Oracle 明确不欢迎的行为。可观察到的后果按严重度:

1. **429 限流**。最常见,退避即可恢复。
2. **软封禁**。持续高频后,该账号的创建请求长时间恒定失败,即使有容量。
3. **账号停用**。有社区报告因自动化申领被停用。无法证实其归因,
   但把频率压低本身没有代价——收益差别很小,风险差别很大。

设计上的应对:

- 默认间隔 60 秒,配置下限 30 秒,**不提供更激进的选项**
- 429 时全账号降速,不是单任务
- 单账号同时最多 1 个运行中的任务;跨账号并发最多 4
- 任务默认 7 天过期,到期自动停并推通知——避免用户建完就忘,
  一个任务在无人看管下跑几个月
- 创建任务时的确认弹窗(L2)必须写明上述风险,不能只是"确定/取消"

**另一件事**:面板持有用户全部账号的私钥。抢机功能会让面板从"偶尔调用 OCI"
变成"持续调用 OCI"。一旦某个账号因此被风控,面板本身会成为怀疑对象——
包括那些没有开抢机的账号,因为它们共用同一个出口 IP。这一点在多账号场景下
尤其值得权衡。

---

## 10. 分阶段实施

| 阶段 | 内容 | 可交付 |
|---|---|---|
| 1 | 任务表 + 调度器 + 错误分类映射 | 后端能跑,无 UI,靠日志观察 |
| 2 | 防重复创建(§6) | **必须在阶段 1 之后立刻做,不能延后** |
| 3 | AD 轮换 + 全账号降速 | 命中率与安全性达标 |
| 4 | UI + 通知 + 过期策略 | 可交付给用户 |

阶段 2 不是"优化",是补一个会静默多创建实例的洞。阶段 1 单独上线是不安全的。

---

## 11. 明确不做

- **跨区域并行**。请求量成倍,收益不明。
- **多账号同一规格并行抢**。等于把风险乘以账号数。
- **秒级轮询**。收益与风险不对称,见 §4。
- **抢到后自动配置**(装 BBR、跑脚本等)。属于另一个功能域,
  `Metadata.user_data` 已经能承载 cloud-init,够用了。
- **伪装来源**(轮换 IP、改 UA、随机化请求特征)。这是规避检测,
  不是本工具该做的事。

---

## 12. 参考

社区里同类实现(仅供对照机制,未逐行审阅):

- `doubleDimple/oci-start` —— 用户此前提供的参考项目,带抢机
- `oci-help`、`oci-arm-host-capacity` —— 同类

核心循环都一样:调 `LaunchInstance`,吃到容量不足就退避重试。
差别全在退避策略、AD 轮换和防重复上——也正是本文着墨最多的地方。
