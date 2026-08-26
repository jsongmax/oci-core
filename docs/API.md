# OCI Core 接口参考

> 后端 v0.2 · 所有接口在 `/api` 下，返回 `application/json; charset=utf-8`。

---

## 约定

### 认证

会话基于 HttpOnly Cookie（`oci_session`，`SameSite=Strict`）。登录后浏览器自动携带，无需手动处理。

### CSRF

**所有写操作（POST / PATCH / PUT / DELETE）必须携带请求头：**

```
X-OCI-Tools: 1
```

浏览器不允许跨源请求携带自定义头（本服务从不开启 CORS），因此"存在该头"即等价于"请求来自本站脚本"。缺失时返回 `403 {"code":"csrf"}`。

读操作不需要该头。

### 错误格式

```json
{
  "code": "oci_error",
  "message": "Out of host capacity.",
  "ociCode": "InternalError",
  "advice": "该可用域暂时没有此规格的容量，可稍后重试或换一个可用域。"
}
```

`ociCode` 是 Oracle 的原始错误码，**必须原样展示且可复制**——用户要拿它去搜索。`advice` 是可直接显示给用户的中文建议。

常见 `code`：

| code | 含义 |
|---|---|
| `unauthenticated` | 未登录 |
| `totp_required` | 已通过口令，待完成两步验证 |
| `csrf` | 缺少 CSRF 请求头 |
| `confirm_required` | 缺少危险操作的确认参数 |
| `instance_busy` | 实例正在状态转换中 |
| `terminate_disabled` | 终止功能已在设置中禁用 |
| `missing_account` | 缺少 `accountId` 查询参数 |
| `quota_exceeded` | 配额不足 |
| `rate_limited` | 登录失败次数过多 |

---

## 认证与初始化

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/status` | 启动时调用，判断该渲染哪个界面 |
| POST | `/api/setup` | 创建第一个用户，**仅在零用户时可用** |
| POST | `/api/auth/login` | 登录 |
| POST | `/api/auth/totp/verify` | 提交两步验证码 |
| POST | `/api/auth/logout` | 登出 |
| GET | `/api/auth/me` | 当前用户 |
| POST | `/api/auth/totp/setup` | 生成 TOTP 密钥与二维码 URI |
| POST | `/api/auth/totp/enable` | 提交验证码完成绑定 |
| POST | `/api/auth/password` | 修改口令（**会使所有会话失效**） |
| POST | `/api/auth/sessions/revoke-all` | 强制全部会话下线（含当前会话） |

**`GET /api/status`**

```json
{ "setupRequired": false, "authenticated": true, "totpRequired": false,
  "totpEnabled": true, "username": "admin", "version": "0.2.0" }
```

**`POST /api/auth/login`** → `{ "totpRequired": true, "totpEnabled": true }`

`totpRequired` 为 `true` 时会话处于「半登录」，除 `/api/auth/totp/verify` 与 `/api/auth/logout` 外一律返回 401 `totp_required`。

登录失败返回 401，**用户名不存在与口令错误的响应完全一致**（防用户名枚举）。同一 IP 连续失败 5 次锁定 15 分钟。

---

## 账号管理

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/accounts` | 账号列表 |
| POST | `/api/accounts` | 添加账号 |
| GET | `/api/accounts/{id}` | 账号详情 |
| PATCH | `/api/accounts/{id}` | 修改账号 / 轮换密钥 |
| DELETE | `/api/accounts/{id}?confirm={alias}` | 删除账号，**需回传别名** |
| POST | `/api/accounts/{id}/check` | 重新校验连通性 |
| GET | `/api/accounts/{id}/regions` | 该账号已订阅的区域 |
| POST | `/api/accounts/parse-config` | 解析粘贴的 OCI 配置 |
| POST | `/api/accounts/check-draft` | 保存前测试凭据 |

**Account 对象**

```json
{
  "id": "a3f1c2d4e5b60718", "alias": "东京主号", "code": "TYO", "colorIndex": 1,
  "tenancyOcid": "ocid1.tenancy.oc1..aaaa", "userOcid": "ocid1.user.oc1..aaaa",
  "fingerprint": "20:3b:97:13:…", "defaultRegion": "ap-tokyo-1",
  "compartmentOcid": "", "proxyUrl": "", "enabled": true,
  "status": "ok", "statusMessage": "", "lastCheckedAt": "2026-08-17T12:00:00Z",
  "subscribedRegions": ["ap-tokyo-1", "ap-osaka-1"], "homeRegion": "ap-tokyo-1",
  "email": "me@example.com", "tenancyName": "my-tenancy",
  "createdAt": "…", "updatedAt": "…"
}
```

`status`：`unchecked` / `ok` / `error`。前端的 `checking` 是本地瞬时态。

> **私钥永不出现在任何响应里，也没有导出接口。** 添加时提交 `privateKeyPem`，此后无法读回。

**`POST /api/accounts/parse-config`** — 添加账号抽屉的核心交互

请求 `{ "text": "<粘贴的整段配置>" }`，响应：

```json
{ "profiles": [{
  "name": "DEFAULT", "userOcid": "…", "fingerprint": "20:3b:…",
  "tenancyOcid": "…", "region": "ap-tokyo-1", "keyFile": "…",
  "hasPassPhrase": false, "complete": true, "missing": [], "suggestedCode": "TOK"
}]}
```

区域三字母代号会自动展开（`nrt` → `ap-tokyo-1`），指纹统一转小写。`hasPassPhrase` 为 `true` 时需提示用户先用 `openssl rsa` 解密——本工具不支持带口令的私钥。

**`POST /api/accounts/check-draft`** — 保存前测试

```json
{ "ok": true,
  "steps": [
    { "key": "pem", "label": "私钥格式有效", "ok": true },
    { "key": "fingerprint", "label": "指纹与私钥匹配", "ok": true, "detail": "20:3b:…" },
    { "key": "identity", "label": "GetUser 调用成功", "ok": true, "detail": "me@example.com" },
    { "key": "regions", "label": "已订阅 4 个区域", "ok": true, "detail": "ap-tokyo-1, …" }
  ],
  "userName": "…", "userEmail": "…", "tenancyName": "…",
  "regions": ["ap-tokyo-1"], "homeRegion": "ap-tokyo-1",
  "errorCode": "", "errorText": "", "advice": "", "accountFatal": false }
```

按 `steps` 顺序逐项点亮即可。

---

## 实例管理

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/instances` | 跨账号聚合列表（读缓存，秒开） |
| GET | `/api/instances/{ocid}` | 实例详情（实时查询） |
| POST | `/api/instances/sync` | 触发同步，`?accountId=` 可限定单账号 |
| POST | `/api/instances/{ocid}/actions/{action}` | 开关机等操作 |
| PATCH | `/api/instances/{ocid}` | 改名 |
| POST | `/api/instances/{ocid}/reshape` | 改配置（**需先关机**） |
| DELETE | `/api/instances/{ocid}?confirm={name}` | 终止，**需回传实例名** |
| POST | `/api/instances/{ocid}/dismiss-error` | 清除该行的错误提示 |
| POST | `/api/instances/{ocid}/change-ip?confirm=true` | 更换公网 IP |
| POST | `/api/instances/{ocid}/enable-ipv6` | 启用 IPv6 |
| GET | `/api/instances/{ocid}/metrics` | 监控数据 |
| POST | `/api/instances/{ocid}/console` | 建立串行控制台连接 |
| POST | `/api/instances/bulk` | 批量开关机 |

**`GET /api/instances`** — 查询参数均可选，多值用逗号分隔

`accountIds` · `regions` · `states` · `search`（匹配名称/OCID/公网IP）· `includeTerminated`

```json
{ "instances": [{
    "id": "ocid1.instance.oc1..aaaa", "accountId": "a3f1c2d4",
    "region": "ap-tokyo-1", "displayName": "arm-tokyo-01",
    "availabilityDomain": "xxxx:AP-TOKYO-1-AD-1", "faultDomain": "FAULT-DOMAIN-1",
    "shape": "VM.Standard.A1.Flex", "ocpus": 4, "memoryGb": 24,
    "lifecycleState": "RUNNING", "publicIp": "152.70.14.208", "privateIp": "10.0.0.5",
    "ipv6": "", "bootVolumeGb": 50, "bootVolumeVpus": 10,
    "timeCreated": "…", "syncedAt": "…", "lastError": "",
    "accountAlias": "东京主号", "accountCode": "TYO", "accountColorIndex": 1
  }],
  "sync": { "syncing": false, "lastSync": "2026-08-17T12:00:00Z" } }
```

账号身份（`accountCode` / `accountColorIndex`）已联表带出，无需二次查询。

已终止实例默认隐藏。`lastError` 非空时在该行浮出错误条，用户确认后调 `dismiss-error`。

**操作**：`{action}` ∈ `START` / `SOFTSTOP` / `SOFTRESET` / `STOP` / `RESET`

`STOP` 与 `RESET` 是拔电源式操作，**必须附带 `?force=true`**，否则返回 `confirm_required`。

响应是更新后的实例，`lifecycleState` **一定是过渡态**（`STARTING` / `STOPPING`），不是终态。落定由 SSE 推送。转换期间再次操作返回 `409 instance_busy`。

**`POST /api/instances/sync`**

```json
{ "startedAt": "…", "durationMs": 2841, "accounts": 8, "regions": 14,
  "instances": 30, "pruned": 1,
  "errors": [{ "accountId": "…", "accountAlias": "新加坡号", "region": "ap-singapore-1",
               "message": "…", "ociCode": "NotAuthenticated", "advice": "…" }] }
```

错误按（账号 × 区域）隔离：**一个账号失效不会让整张列表变空**。

**`POST /api/instances/{ocid}/reshape`** — `{ "ocpus": 2, "memoryInGbs": 12 }`

实例非 `STOPPED` 时返回 400 并说明需先关机。OCPU/内存搭配按规格元数据实时校验（A1.Flex 每 OCPU 最多 6 GB）。

**`GET /api/instances/{ocid}/metrics`** — `?hours=6&metrics=CpuUtilization,NetworksBytesIn`

```json
{ "instanceId": "…", "start": "…", "end": "…", "resolution": "5m",
  "series": [{ "metric": "CpuUtilization", "aggregation": "mean",
               "datapoints": [{ "timestamp": "…", "value": 3.2 }], "error": "" }],
  "notice": "监控数据依赖实例内运行的 Oracle Cloud Agent。…" }
```

流量类指标自动使用 `rate` 聚合。采样粒度随时间跨度自动变粗。**所有序列为空是正常的**——没装 Cloud Agent 就没有数据，需与"调用失败"区分显示。

**`POST /api/instances/{ocid}/console`** — `{ "publicKey": "ssh-ed25519 AAAA…" }`

Oracle 用提交的公钥鉴权，**本工具不代管任何 SSH 私钥**。同一实例已有活跃连接时直接复用，不会撞 409。

```json
{ "id": "ocid1.instanceconsoleconnection…", "lifecycleState": "ACTIVE",
  "serialConsoleCommand": "ssh -o ProxyCommand=…", "vncConsoleCommand": "ssh -L 5900:…",
  "notice": "在本机终端执行上述命令即可连接。…" }
```

**`POST /api/instances/bulk`** — `{ "instanceIds": [...], "action": "SOFTSTOP", "force": false }`

单次最多 20 台，并发度 3（同租户并发太高容易触发限流）。**不支持批量终止**——那是 L3 操作，必须逐台输名确认。受 `allowBulkActions` 策略控制。

```json
{ "results": [{ "instanceId": "…", "name": "web-01", "ok": true }],
  "succeeded": 3, "failed": 1 }
```

---

## 实时事件流（SSE）

```
GET /api/events
```

`text/event-stream`，每 25 秒一个 `: ping` 心跳。事件类型：

| type | 触发时机 |
|---|---|
| `instance.updated` | 实例状态或网络信息变化 |
| `instance.removed` | 实例已终止并从缓存移除 |
| `instance.error` | 操作失败 |
| `account.status` | 账号连通性变化 |
| `sync.started` / `sync.finished` | 同步开始 / 结束 |

```
event: instance.updated
data: {"type":"instance.updated","at":"…","instanceId":"ocid1…","accountId":"…","state":"RUNNING"}
```

浏览器 `EventSource` 自带断线重连。**重连后应重新拉一次全量列表**——事件不做持久化补发。

---

## 创建实例

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/launch/presets` | 免费额度快捷预设 |
| GET | `/api/launch/availability-domains` | 可用域 |
| GET | `/api/launch/shapes` | 规格（已去重） |
| GET | `/api/launch/images` | 镜像，**建议带 `?shape=` 过滤** |
| POST | `/api/launch` | 创建 |

除 presets 外均需 `?accountId=`，`?region=` 可选（默认账号主区域）。

> **镜像必须按 shape 过滤**：ARM 与 x86 的镜像不通用，不过滤会让用户选到一个根本起不来的镜像。

**`POST /api/launch`**

```json
{ "accountId": "a3f1c2d4", "region": "ap-tokyo-1", "availabilityDomain": "",
  "displayName": "arm-tokyo-02", "shape": "VM.Standard.A1.Flex",
  "ocpus": 4, "memoryInGbs": 24, "imageId": "ocid1.image…", "bootVolumeGb": 50,
  "subnetId": "", "autoCreateNetwork": true, "assignPublicIp": true, "enableIpv6": false,
  "sshPublicKey": "ssh-rsa AAAA…", "cloudInit": "#cloud-config\n…" }
```

`availabilityDomain` 留空自动选第一个。`subnetId` 留空且 `autoCreateNetwork: true` 时自动创建 VCN + 网关 + 路由 + 子网。`cloudInit` 提交原文，服务端负责 base64 编码。

创建前会做配额预检，不足时返回 400 `quota_exceeded`。

响应 201：

```json
{ "instance": { …LifecycleState 为 PROVISIONING… },
  "steps": ["已创建 VCN OCI Core-vcn (10.0.0.0/16)", "已配置默认路由 0.0.0.0/0", "…"],
  "notice": "实例正在创建，通常需要 1–3 分钟。公网 IP 会在就绪后出现。" }
```

就绪由 SSE 的 `instance.updated`（`state: RUNNING`）推送。

---

## 网络

| 方法 | 路径 |
|---|---|
| GET | `/api/network/vcns` |
| GET | `/api/network/subnets` `?vcnId=` |
| GET | `/api/network/security-lists` `?vcnId=` |
| PUT | `/api/network/security-lists/{id}` |
| GET | `/api/network/rule-templates` |
| GET | `/api/network/public-ips` `?scope=REGION\|AVAILABILITY_DOMAIN` |
| POST | `/api/network/ensure` `?ipv6=true` |

均需 `?accountId=`。

> **`PUT /api/network/security-lists/{id}` 是整体替换语义，不是增量追加。** 必须先 GET 拿到完整规则集，在其基础上修改后整体提交，否则未提交的规则会被静默删掉。

列表响应会额外标注 `allowAllRules: [索引]`，指出哪几条等同于全放行，需在 UI 上显著警示。

**`POST /api/instances/{ocid}/change-ip?confirm=true`**

不带 `confirm=true` 返回 `confirm_required` 与后果说明。保留 IP 会被拒绝（返回 `reserved_ip`）——删掉就永久释放了。

---

## 存储

| 方法 | 路径 |
|---|---|
| GET | `/api/storage/boot-volumes` `?availabilityDomain=` |
| PATCH | `/api/storage/boot-volumes/{id}` |
| GET | `/api/storage/volumes` |
| PATCH | `/api/storage/volumes/{id}` |
| POST | `/api/storage/boot-volume-attachments/detach` `?attachmentId=` |
| POST | `/api/storage/boot-volume-attachments/attach` `?instanceId=&bootVolumeId=` |

> **分离引导卷用的是「挂载关系 OCID」，不是「卷 OCID」。** 两者混用会直接 404。
> 挂载关系 ID 在实例详情的 `bootVolumeAttachmentId` 字段里。
> 这组接口是「救援模式」：把起不来的机器的系统盘卸下来，挂到另一台正常实例上修。实例必须先关机。

请求体 `{ "displayName": "", "sizeInGbs": 100, "vpusPerGb": 20 }`，均可选。

扩容只能增不能减，VPU 只能是 0 或 10–120 之间 10 的倍数，两者都在服务端提前校验。

扩容成功后响应带 `notice`：**云盘扩容后还需登录实例扩展分区与文件系统**，否则用户会以为没生效。

---

## 对 Oracle 的并发限制

出站请求过两级信号量，都在 `ociclient` 里：

| 级别 | 默认 | 防的是什么 |
|---|---|---|
| 每租户 | 6 | Oracle 按租户限流。堆太多并发换来的是 429 与退避，实际吞吐更低 |
| 全局 | 16 | 多账号聚合时的总量。这一级防的不是 Oracle，是本机的 fd 与出口带宽 |

全局这一级由 `ociconn.Factory` 持有并注入每个客户端——**跨账号的总量只有
在这里才拦得住**。各操作内部原有的信号量（同步 6、配额 4、批量 3）限制的是
单次操作的扇出宽度，两者不互相替代：那些信号量拦不住「同步 + 配额 + 批量
同时触发」叠加出来的总量。

排队时尊重 context：用户关掉页面，排队中的请求立刻松手，不会占着名额
把后面的请求堵在外面。

## 账号类型

连通性校验会顺带查一次订阅信息，写入账号记录：

```json
{ "paymentModel": "FREE_TRIAL", "subscriptionState": "ACTIVE",
  "subscriptionEndsAt": "2026-09-12T07:59:59+08:00" }
```

来源是 `GET https://organizations.{region}.oci.oraclecloud.com/20230401/subscriptions?compartmentId={tenancy}`。
`paymentModel` 为 `FREE_TRIAL` 即试用号，`PAY_AS_YOU_GO` 即升级号；
试用订阅的 `endDate` 就是试用到期日。

需要 organizations 服务的读权限，精简权限的 IAM 用户会拿到 401/404。
**取不到时三个字段留空，前端显示"类型未知"——不要回退成猜测。**

> **不能拿配额值反推账号类型。** 试用期内的账号拿到的限额远高于永久免费
> 额度（实测 ARM 16 OCPU / 96 GB），按配额判断会把试用号认成升级号，
> 正好认反了那个需要提醒用户的方向——试用到期那天配额降回永久免费额度，
> 超出的实例会被 Oracle 回收。

### 永久免费额度

**Oracle 会不打招呼地改这些数字。** 2026-06-15 起 Ampere A1 从
4 OCPU / 24 GB 砍到 **2 OCPU / 12 GB**，没有公告，只给用户发了邮件，
并从 2026-08-18 起终止超出新限额的永久免费实例。

因此项目里只有两个定义处，且都标注了核对日期：

- `internal/ociclient/freetier.go`
- `web/src/lib/freetier.ts`

**这些常量只用于预设与提示文案，绝不用于校验。** 真实上限一律以 limits
接口返回的值为准——那是账号自己的数字，不会因为本表滞后而出错。
`ValidateShapeConfig` 同理，只用 shape 自带的元数据。

---

## 配额

**`GET /api/quota`** — `?accountId=` 限定单账号，`?refresh=true` 跳过缓存（默认缓存 5 分钟）

```json
{ "quotas": [{ "accountId": "a3f1c2d4", "region": "ap-tokyo-1",
    "items": [
      { "key": "ocpu",  "name": "standard-a1-core-regional-count",
        "label": "ARM OCPU", "used": 3, "limit": 4, "known": true },
      { "key": "micro", "name": "vm-standard-e2-1-micro-count",
        "label": "AMD 微型实例", "used": 2, "limit": 2, "known": true },
      { "key": "block", "name": "total-free-storage-gb-regional",
        "label": "块存储 (GB)", "used": 150, "limit": 200, "known": true }
    ],
    "error": "", "fetchedAt": "…" }] }
```

`key` 取值 `ocpu` / `memory` / `micro` / `block`。

> **前端按 `key` 取值，不要匹配 `name`。** `name` 是 Oracle 的限额名，
> 换一个就会整片静默显示成 0/0——看起来像"配额真的是零"，而不是"匹配失败"。

> **`known: false` 必须显示为「未知」而不是 0。** 把未知画成 0 会让用户以为还有额度。
> 此时 `error` 字段给出原因（权限不足、该区域无此限额等）。

限额的作用域分 REGION 与 AD 两种。AD 作用域的限额查用量时必须逐个可用区查再求和，
不带可用区会得到 400 `InvalidParameter`；REGION 作用域反过来，带了才会被拒。

---

## 身份域密码策略

租户的控制台密码默认 **120 天到期**，到期必须重置。管着几个只是「留着别被回收」
的账号时，这意味着每隔几个月就得挨个登进去改一次。

> **这个设置不在经典 IAM 里。** `AuthenticationPolicy` 只管密码长度与字符类型，
> **没有有效期字段**。有效期属于 **Identity Domains** 的 `PasswordPolicy`，
> 端点是每个身份域独立的 URL，走 SCIM 风格接口。
>
> 好在身份域的 REST API 支持 OCI 请求签名，所以本工具复用同一个签名器，
> 不需要另做一套 OAuth。

**`GET /api/accounts/{id}/password-policy`**

```json
{ "policy": {
    "accountId": "a3f1c2d4", "supported": true,
    "domainName": "Default", "domainUrl": "https://idcs-….identity.oraclecloud.com",
    "policyId": "…", "policyName": "Default",
    "expiresAfterDays": 120, "warnBeforeDays": 7, "minLength": 8 },
  "notice": "…" }
```

`supported: false` 表示该租户**没有身份域**（未迁移的老租户），压根不存在密码
有效期这回事。这不是错误，界面应当照实说明而不是报错。

**`PATCH /api/accounts/{id}/password-policy`** — `{"days": 365}` 或 `{"disable": true}`

`disable` 走 SCIM 的 `remove` 操作移除该属性。

> **Oracle 未公开说明哪个值代表「永不过期」。** 因此本工具改完之后会**立即回读**
> 一次，返回并展示服务端实际存下的值，而不是提交的值。若回读发现仍有有效期，
> 响应的 `notice` 会明说，让用户改为设置一个较大的天数——
> **提交成功不等于生效，这两件事必须分开讲。**

用 PATCH 而非 PUT：这个资源有六十多个字段，PUT 要整体回传，少传一个就可能被清掉。

> **权限门槛高。** 需要身份域管理员，远超本工具其余功能所需的 compute /
> network / volume。这条策略作用于**整个身份域**，影响该租户下所有用户的
> 控制台登录，不只是本工具用的那个 IAM 用户。修改会写入审计日志。

> 关掉过期本身不降低安全性：NIST SP 800-63B 已不推荐强制定期轮换，理由是
> 它逼着人用可预测的变体。真正管用的是强密码加多因子。

---

## 代理池

给每个账号配一条独立出口，让各账号的 API 调用不从同一个 IP 出去。

> **代理只换 IP，不换身份。** 每个 OCI 请求都带该账号的私钥签名，
> Oracle 始终知道是哪个租户在调。它降低的是「多个账号同 IP」这一个信号，
> 不是万能的防关联手段。

**`GET /api/proxies`**

```json
{ "proxies": [
    { "id": "a1b2", "label": "香港", "scheme": "socks5", "host": "1.2.3.4", "port": 1080,
      "username": "alice", "hasPassword": true, "enabled": true,
      "lastStatus": "ok", "lastLatencyMs": 118, "lastError": "",
      "lastRegion": "ap-tokyo-1", "lastCheckedAt": "…", "lastOkAt": "…" }
  ],
  "bindings": { "<accountId>": "<proxyId>" },
  "checkTimeoutMs": 10000, "notice": "…" }
```

> **密码永不回传。** 只有 `hasPassword` 说明配没配。密码以 AES-256-GCM
> 加密落库，AAD 绑定该行 id——与 OCI 私钥同等待遇。

**`POST /api/proxies/import`** — `{ "text": "...", "dryRun": true }`

逐行解析，认这几种写法：

```
1.2.3.4:8080
1.2.3.4:8080:user:pass          ← 代理商最常给的
user:pass@1.2.3.4:8080
socks5://user:pass@1.2.3.4:1080  # 行尾注释成为备注名
```

不写协议默认 `http`。**不支持 `socks5h`**（Go 的 net/http 只支持 `socks5`），
会显式报错而不是留到运行时神秘失败。

`dryRun` 为真时只解析不落库，返回逐行结果供预览。失败行保留**原始行号**——
粘二十行进来只报个总数，等于让用户自己找。

**`POST /api/proxies/bind`** — `{ "accountId": "...", "proxyId": "..." }`

`proxyId` 为空串表示解绑，回到本机直连。

> **一条代理只能绑一个账号。** 重复绑定返回 409 `proxy_shared`，是硬拒绝不是警告：
> 两个账号共用同一出口，等于把它们绑在同一个 IP 上，凭空制造一个本来不存在的
> 关联信号——与网络隔离的目的正好相反。

**`POST /api/proxies/check`** / **`POST /api/proxies/{id}/check`**

通过该代理向 `https://iaas.{region}.oraclecloud.com` 发一个未认证请求，
拿到任何 HTTP 响应即算通。

选 OCI 而不是第三方 IP 回显服务，理由有三：测的是真正要走的那条路；
不把代理列表送给任何第三方；不需要凭据、不消耗配额、不产生费用。

打哪个区域按该代理**所绑账号的 home region** 定——一条美国代理连东京和连
阿什本的延迟差好几倍，固定测一个端点给出的数字是误导。未绑定的代理用
用户任一账号的区域，都没有时回落 `us-ashburn-1`。

> **检测不了「不同代理但同一出口 IP」。** 那需要第三方回显服务。
> 代理商给的多条 IP 是否真的独立，需要自行验证。

**`PATCH /api/proxies/{id}`** — 改备注、启停、重设密码（空串清除）
**`DELETE /api/proxies/{id}`** — 仍被绑定时返回 409 `proxy_in_use`

> 删除前必须先解绑。静默解绑会让那个账号在用户不知情的情况下回落本机直连——
> 而用代理的全部目的就是不要那样。同理，建连时**不做任何失败回落**：
> 代理取不到或解不开就直接让调用失败。

---

## 账单

用量与成本，数据来自 OCI Usage API（`RequestSummarizedUsages`）。**只读接口，查询本身不产生费用。**

需要一项本工具其余功能都用不到的权限：

```
Allow group <你的组> to read usage-report in tenancy
```

已有 `read all-resources in tenancy` 的账号无需再加，那条已覆盖。
缺这项权限时接口**不报错**，而是把该账号的 `status` 置为 `no_permission`。

**`GET /api/billing`** — `?accountId=` 限定单账号，`?refresh=true` 跳过缓存（默认缓存 30 分钟）

```json
{ "summaries": [
    { "accountId": "a3f1c2d4", "status": "ok", "currency": "USD",
      "thisMonth": 6.4231, "lastMonth": 4.1, "region": "ap-tokyo-1", "fetchedAt": "…" },
    { "accountId": "b7e2…", "status": "free", "currency": "",
      "thisMonth": 0, "lastMonth": 0, "region": "ap-osaka-1", "fetchedAt": "…" },
    { "accountId": "c9d1…", "status": "no_permission", "region": "eu-frankfurt-1", "fetchedAt": "…" }
  ],
  "totals": [{ "currency": "USD", "thisMonth": 6.4265, "lastMonth": 4.1, "accounts": 3 }],
  "countedAccounts": 3, "noPermissionCount": 1, "notice": "…" }
```

`status` 取值：

| 值 | 含义 |
|---|---|
| `ok` | 查到费用，金额大于零 |
| `free` | 查到数据但金额为零——免费额度内，**不是错误** |
| `no_permission` | 缺 `read usage-report` 权限，**不是账号故障** |
| `disabled` | 账号在本面板里被停用 |
| `error` | 其余失败，`error` 字段给原因 |

> **`free` 与 `no_permission` 必须分开显示。** 前者是"确实没花钱"，
> 后者是"不知道花没花钱"，含义正好相反。合成一个状态会让用户误以为账号免费。

> **`totals` 按币种分组，不给单一总数。** 不同账号可能结算在不同币种，
> 把 USD 和 CNY 加成一个数字是错的，而且错得看不出来。只有 `ok` 与 `free`
> 的账号计入合计——把查不到的账号计成 0 会让总数看着像"这几个账号没花钱"。

**`GET /api/billing/{accountId}`** — `?days=` 天数（默认 30，上限 180），`?refresh=true` 跳过缓存

```json
{ "accountId": "a3f1c2d4", "status": "ok", "currency": "USD", "region": "ap-tokyo-1",
  "start": "2026-07-22T00:00:00Z", "end": "2026-08-21T00:00:00Z", "days": 30,
  "total": 8.7104,
  "series":   [{ "date": "2026-07-22", "amount": 0.4312 }, { "date": "2026-07-23", "amount": 0 }],
  "services": [{ "key": "计算", "amount": 6.0973, "quantity": 0, "unit": "" }],
  "regions":  [{ "key": "ap-tokyo-1", "amount": 6.9683, "quantity": 0, "unit": "" }],
  "usage":    [{ "key": "计算", "amount": 0, "quantity": 2880, "unit": "OCPU Hours" }],
  "fetchedAt": "…" }
```

`series` 已补齐没有用量的日子（`amount: 0`）。Oracle 只返回有用量的天，
不补齐会把 8 月 3 日和 8 月 9 日排成相邻两根柱子，让断续的曲线看起来是连续的。

`usage` 是同一区间的**用量**视角（`queryType=USAGE`），金额恒为零的免费账号
只有这一块有内容。用量为零的服务已被过滤——免费号的响应里能有几十条零用量记录。

> **数据有延迟。** Oracle 每隔几小时结算一次，最新一天通常不完整。
> 这个接口给不出"实时消费"，界面上不要那样描述。

> **发票与付款记录不在这里。** 那部分只在 Oracle 官网的账户中心，
> 走的不是 OCI 的签名体系，没有可用的接口。

---

## 通知与设置

| 方法 | 路径 |
|---|---|
| GET | `/api/notifications/channels` |
| POST | `/api/notifications/channels` |
| PATCH | `/api/notifications/channels/{id}` |
| DELETE | `/api/notifications/channels/{id}` |
| POST | `/api/notifications/channels/{id}/test` |
| GET | `/api/notifications/events` |
| GET / PATCH | `/api/settings` |

渠道类型：`telegram` · `wecom` · `dingtalk` · `email` · `webhook`。列表响应同时返回 `kinds`，含每种渠道的字段定义（`key` / `label` / `required` / `secret` / `hint`），**前端据此动态渲染配置表单**，无需硬编码。

> **机密字段（`token` / `password` / `secret` / `webhook`）在响应中打码**，形如 `1234••••••••WXYZ`。
> 提交时若某字段仍是打码值，服务端保留原值——直接把读到的配置回填提交是安全的。

**`POST …/test`** 立刻发送并返回结果，始终 200：

```json
{ "ok": false, "error": "渠道返回错误 93000: invalid webhook url" }
```

**可订阅事件**：`instance.anomaly` · `account.auth` · `quota.limit` · `disk.full` · `instance.created` · `danger.operation`

**`GET /api/settings`**

```json
{ "allowTerminate": true, "allowBulkActions": true,
  "requireTotpForDanger": false, "syncIntervalMinutes": 5,
  "checkIntervalHours": 6 }
```

`allowTerminate: false` 时终止接口直接返回 403 —— **服务端强制，前端按钮可以被绕过**。

`checkIntervalHours` 是自动重跑凭据校验的间隔，`0` 为关闭，范围 0–168。
巡检每分钟醒一次查看有没有到期的账号，因此改动一分钟内生效，无需重启
（`syncIntervalMinutes` 则要重启）。每轮最多校验 2 个账号，避免批量导入的
账号到期时刻挤在一起、同一秒向 Oracle 发出几十个请求。

状态发生变化时才推 `account.status` 事件与通知——每 6 小时播报一次
「还是好的」只会让用户学会忽略通知。

---

## 总览与审计

**`GET /api/overview`**

```json
{ "accounts": { "total": 8, "ok": 6, "error": 1, "disabled": 1 },
  "instances": { "total": 30, "byState": { "RUNNING": 26, "STOPPED": 4 } },
  "distribution": [{ "accountId": "…", "region": "ap-tokyo-1", "count": 6 }],
  "regions": ["ap-tokyo-1", "…"],
  "sync": { "syncing": false, "lastSync": "…" },
  "attention": [{ "kind": "account_error", "accountId": "…", "target": "法兰克福号",
                  "message": "401 NotAuthenticated", "severity": "danger" }] }
```

`attention` 为空时整块隐藏——无异常时不该占用注意力。

**`GET /api/audit`** — `?accountId=` `?action=` `?limit=`（默认 200，上限 500）

---

## 危险操作分级速查

| 级别 | 操作 | 服务端门槛 |
|---|---|---|
| L1 | 开机 / 软关机 / 软重启 | 无 |
| L2 | 强制关机、强制重启 | `?force=true` |
| L2 | 更换公网 IP | `?confirm=true` |
| L3 | **终止实例** | `?confirm={实例名}` + `allowTerminate` 未禁用 |
| L3 | **删除账号** | `?confirm={账号别名}` |

前端的确认框是体验，**这些校验才是防线**。
