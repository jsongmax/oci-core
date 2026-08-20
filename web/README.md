# OCI Core web

按 `FRONTEND-DESIGN.md` v2.0 实现的前端工程。Vue 3 + TypeScript + Vite，产物可 embed 进 Go 单二进制。

## 运行

```bash
cd vue
npm install
npm run dev        # http://localhost:5173
npm run build      # 输出到 ../internal/web/dist
npm run typecheck
```

Vite 已配置 `base: './'` 与 hash 路由，Go 侧用 `embed.FS` 直接托管 `dist` 即可，无需 SPA rewrite。
`/api` 在 dev 下代理到 `127.0.0.1:8080`。

## 目录

```
src/
  styles/tokens.css      §5 设计令牌（Light/Dark 双 Mode + 8 身份色 + 4 套强调色）
  styles/base.css        基础样式、通用类、§7 全部 keyframes、reduced-motion 降级
  types.ts               Account / Instance / 六态 / 确认请求等类型
  lib/lifecycle.ts       §6.2 六态元数据（色、实心/空心、是否过渡态）
  lib/format.ts          配额语义色、count-up、复制、身份色取值
  data/                  演示数据（8 账号 / 30 实例 / 6 区域）—— 换成 API 调用即可
  store/index.ts         reactive 全局状态 + 动作（含 §6.3 过渡态时序）
  router/index.ts        路由（含 /m/* 移动端）
  components/            通用与业务组件
  views/                 页面
  views/drawers/         四个抽屉
  views/mobile/          移动端两屏
```

## 规格书条目对照

| 规格 | 实现位置 |
|---|---|
| §5.1–5.8 令牌 | `styles/tokens.css`，组件内一律 `var(--*)`，无硬编码色值 |
| §5.2 身份色 + 短代号 | `AccountChip.vue`，实例表/网络/存储/日志/搜索每行都用 |
| §6.1 AccountChip | `components/AccountChip.vue`（dot / chip / full + 异常叠加） |
| §6.2 StateBadge | `components/StateBadge.vue`（仅过渡态脉冲，实心/空心第二重编码） |
| §6.3 LifecycleTransition | `store/index.ts` → `runTransition()`：乐观更新只到过渡态、202 后进度条、落定 spring、失败可见回滚 |
| §6.4 InstanceRow | `views/InstancesView.vue`（hover 提亮色条、选中 accent 条、两种密度） |
| §6.5 三级门槛 | `components/ConfirmDialog.vue` + `InstanceRowActions.vue`（L3 输名、L2 取消为主按钮） |
| §6.6 CredentialField | `components/CredentialField.vue` |
| §6.7 QuotaMeter | `components/QuotaMeter.vue` |
| §6.8 ShapeConfigurator | `components/BootVolumeEditor.vue`（双滑块联动 + 配额校验 + 关机前置提示） |
| §7 Tier 1 | 脉冲、不定进度条、落定 spring + 行高光、按钮 spinner、KPI count-up、骨架屏、色条 hover 提亮 |
| §7 Tier 2 | 玻璃拟态、登录/总览光晕、侧栏磁吸指示器、抽屉滑入、图表渐变、分组折叠 |
| §7 Tier 3 | `instance-ready`：扫光 + 徽章回弹 + IP 浮现 + toast 带 SSH 复制，≤1.2s |
| §7 性能红线 | 只动画 transform/opacity；`MAX_CONCURRENT_PULSES = 12`；reduced-motion 与「减弱动效」全量降级 |
| §9 响应式 | 各视图内的媒体查询（1599 / 1279 / 1023 / 767 断点）+ `/m/*` 移动端 |
| §10 四态 | `SkeletonRows` / `EmptyState` / 错误保留原始错误码 / 权限自检列出缺失权限 |
| §12 不在本期 | 未预留抢机相关视觉元素与入口 |

## 接后端时要改的地方

1. `src/data/*.ts` 换成 `GET /api/accounts`、`GET /api/instances`（字段与 `types.ts` 一致）。
2. `store.runTransition()` 内的 `setTimeout` 换成真实 `POST /api/instances/:id/actions` + 轮询 `GET /api/instances/:id`，
   失败分支已经写好可见回滚，直接接错误码即可。
3. `AddAccountDrawer.runTest()` 换成 `POST /api/accounts/validate`，逐项结果流式返回或分次请求。
4. 后台标签页应暂停轮询：`document.visibilityState` 监听接在轮询入口处。
