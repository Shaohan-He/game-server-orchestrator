<p align="center">
  <h1 align="center">🎮 Game Fleet Director</h1>
  <p align="center"><strong>游戏服舰队生命周期管理 / Game Server Fleet Lifecycle Manager for Kubernetes</strong></p>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/go-1.22%2B-00ADD8" alt="Go 1.22+">
  <img src="https://img.shields.io/badge/kubernetes-1.28%2B-326CE5" alt="K8s 1.28+">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License">
  <img src="https://img.shields.io/badge/platform-linux%2Famd64-lightgrey" alt="Linux/amd64">
  <img src="https://github.com/Shaohan-He/game-server-orchestrator/actions/workflows/ci.yml/badge.svg" alt="CI">
</p>
##该项目已被放弃,仅供参考!!!

---


## 目录 / Table of Contents

- [概述](#概述)
- [快速开始](#快速开始)
- [架构](#架构)
- [部署前必改配置](#部署前必改配置)
- [功能模块](#功能模块)
- [CRD 设计](#crd-设计)
- [配置说明](#配置说明)
- [开发](#开发)
- [贡献](#贡献)
- [许可证](#许可证)
- [English](#english)



## 概述

**Game Fleet Director** 是 K8s Operator，管理游戏服的扩缩容、排水和分配。通用 HPA 不懂游戏逻辑——一个满员服的 40% CPU 要保护，一个空闲服的 50% CPU 可以回收。HPA 也不懂玩家在线——一直接 `kubectl delete pod` 会踢人下线。

Game Fleet Director 理解这些语义：以玩家数为主要伸缩指标、三阶段排水不丢一个玩家、预热暖池让匹配器即时分配。

### 它在工具链中的位置

Game Fleet Director 是 [三部曲](https://github.com/Shaohan-He) 的**第三环**——"行动层"：

| 项目 | 语言 | 回答的问题 |
|------|------|-----------|
| [Node Guardian](https://github.com/Shaohan-He/node-guardian) | Bash | 出了故障怎么排查和修复？ |
| [Node Health Watcher](https://github.com/Shaohan-He/node-health-watcher) | Python | 什么时候该去排查？ |
| **Game Fleet Director** ← 你在这里 | Go | 谁来操作游戏服本身？ |

** Go + K8s Operator ** controller-runtime 是社区标准库，CRD + Controller 模式天然适合 GitOps。选 Operator 而非 CronJob 是因为排水需要状态机跨 reconcile 周期持久化，CronJob 的无状态模型承载不了。

### 核心原则

| 原则 | 实现 |
|------|------|
| **玩家感知伸缩** | 以在线玩家数为主维度决策，CPU/内存仅作辅助参考 |
| **优雅排水** | Cordon → Drain → Decommission 三阶段，绝不暴力杀有人的服 |
| **暖池** | N 台预热就绪的游戏服等着，匹配器拿来就用，零冷启动 |
| **声明式** | 三个 CRD：Fleet / Policy / Allocation，`kubectl apply` 搞定一切 |
| **节点健康感知** | 集成 NHW 端点，扩缩容自动跳过不健康节点 |
| **防御性** | 干运行、令牌桶限流、熔断器，单次 reconcile 失败不阻塞后续 |

---

## 快速开始

```bash
git clone https://github.com/Shaohan-He/game-server-orchestrator.git && cd game-server-orchestrator
go mod tidy && make build

# 编辑 Makefile 的 REGISTRY 为你的镜像仓库地址，然后构建推送
make docker-build TAG=v0.1.0 && make docker-push TAG=v0.1.0

# 安装 CRD + 部署 Controller/API Server
kubectl apply -f config/crds/
kubectl apply -k deploy/

# 部署示例舰队（先编辑 config/samples/fleet-demo.yaml 的 image）
kubectl create namespace game-fleet-demo
kubectl apply -f config/samples/fleet-demo.yaml

# 干运行模式——预览扩缩容决策
kubectl annotate gameserverfleet battle-royale-fleet \
  director.gamefleet.io/dry-run="true" --overwrite

# 查看舰队状态和日志
kubectl get gameserverfleets -n game-fleet-demo
kubectl logs -n game-fleet-system deploy/game-fleet-director-controller -f
```

**环境：** Go 1.22+ · K8s 1.28+ · 容器镜像仓库 · Prometheus（可选）· Node Health Watcher（可选）

## 架构

```
.
├── api/v1alpha1/               # CRD 类型定义
│   ├── gameserverfleet_types.go
│   ├── autoscalerpolicy_types.go
│   └── gameserverallocation_types.go
├── cmd/
│   ├── controller/main.go      # Operator 入口（Manager + Reconciler 注册）
│   └── apiserver/main.go       # REST API 入口（匹配器调用）
├── pkg/
│   ├── controller/             # FleetReconciler + AllocationReconciler + Scaler
│   ├── drainer/                # 三阶段排水状态机 + 会话跟踪
│   ├── pool/                   # 暖池管理器 + 四种分配策略
│   ├── health/                 # NHW API 客户端 + 节点健康过滤
│   ├── metrics/                # Prometheus 指标采集与抓取
│   ├── api/                    # REST API Server（allocate / release / status）
│   └── notifier/               # 飞书 + 钉钉通知
├── config/                     # CRD manifests / RBAC / 示例资源
├── deploy/                     # Kustomize 部署清单
├── tests/                      # 集成测试 (envtest) + E2E
└── .github/workflows/ci.yml    # CI：lint → test → build → integration
```

### 设计决策

**为什么以玩家数为主指标？** CPU 和内存反映的是资源消耗，不是业务需求。一个满员服 CPU 40% 不该缩，一个空服 CPU 30% 可以缩。玩家数才是游戏服的"负载"定义。

**为什么排水是三阶段而不是两阶段？** 两阶段（标记不可分配 → 等待删 Pod）漏了一环：你需要在停止新玩家进入后，等待当前对局结束。三阶段多出一个 Drain 阶段专门等会话清零。

**为什么有暖池？** 游戏服 Pod 冷启动 30s+，玩家匹配进去等的体验不可接受。提前维护 N 台就绪服，匹配器 Allocate 即用。

**节点健康感知** —— Controller 扩缩容前查询 NHW 的 `/api/v1/nodes`，过滤掉 CRITICAL 节点。NHW 不可用时降级为不过滤，不影响核心伸缩逻辑。

---

## 部署前必改配置

以下是 **fork 或 clone 本仓库后、正式部署前必须修改** 的配置项。仓库中的默认值仅为占位，不适用于任何生产或开发环境。

### 1. 容器镜像仓库地址（必改）

**涉及文件：`Makefile`、`deploy/controller.yaml`、`deploy/apiserver.yaml`、`deploy/kustomization.yaml`**

```makefile
# Makefile 第 5 行 —— 替换为你的实际镜像仓库
REGISTRY ?= registry.cn-beijing.aliyuncs.com/你的名称空间
```

```bash
# 更新 kustomization 中的镜像引用（或直接编辑 deploy/kustomization.yaml）
cd deploy
kustomize edit set image \
  registry.cn-beijing.aliyuncs.com/你的名称空间/game-fleet-director-controller:v0.1.0
kustomize edit set image \
  registry.cn-beijing.aliyuncs.com/你的名称空间/game-fleet-director-apiserver:v0.1.0
```

> **国内云厂商镜像仓库地址格式：**
> - 阿里云 ACR：`registry.cn-<地域>.aliyuncs.com/<命名空间>`
> - 腾讯云 TCR：`ccr.ccs.tencentyun.com/<命名空间>`
> - 华为云 SWR：`swr.cn-<地域>.myhuaweicloud.com/<命名空间>`

### 2. Go 模块代理（国内必配）

**涉及文件：`Makefile`**

```makefile
# Makefile 第 9 行 —— 国内用户使用 goproxy.cn，海外用户可设为 direct 或删除此行
export GOPROXY ?= https://goproxy.cn,direct
```

### 3. 镜像拉取凭证（私有仓库必配）

**涉及文件：`deploy/controller.yaml`**

```bash
# 创建 Secret
kubectl create secret docker-registry acr-secret \
  --docker-server=你的仓库地址 \
  --docker-username=你的用户名 \
  --docker-password=你的密码 \
  -n game-fleet-system

# 然后取消 deploy/controller.yaml 中的注释:
#       imagePullSecrets:
#         - name: acr-secret
```

### 4. 游戏服镜像（必改）

**涉及文件：`config/samples/fleet-demo.yaml`**

```yaml
# 第 52 行 —— 替换为你的实际游戏服镜像
image: registry.cn-beijing.aliyuncs.com/游戏项目/game-server:v1.0.0
```

> 游戏服需实现两个 HTTP 端点：
> - `GET :8080/healthz` — 返回 200 表示就绪
> - `GET :8080/api/v1/sessions` — 返回 `{"activeSessions": <int>}` 供排水协议查询
> - `GET :8080/api/v1/metrics` — 返回 `{"players": <int>, "cpuPercent": <float>, "memoryMB": <float>}` 供指标采集

### 5. 通知渠道（可选）

**涉及文件：`cmd/controller/main.go` 启动参数 或 `deploy/controller.yaml` 环境变量**

| 你想启用的通知 | 需修改的位置 | 获取方式 |
|--------------|-------------|---------|
| 飞书 | CLI `--notify-feishu-url` 或环境变量 `GFD_NOTIFY_FEISHU_URL` | 飞书群 → 设置 → 群机器人 → 添加自定义机器人 → 复制 Webhook URL |
| 钉钉 | CLI `--notify-dingtalk-url` 或环境变量 `GFD_NOTIFY_DINGTALK_URL` | 钉钉群 → 设置 → 智能群助手 → 添加机器人 → 自定义 → 复制 access_token |

### 6. 节点健康感知（可选）

**涉及文件：`AutoscalerPolicy` CR 的 `spec.nodeHealth` 字段**

```yaml
nodeHealth:
  enabled: true
  provider: nhw
  nhwEndpoint: "http://node-health-watcher.nhw-system:8080"  # 改为你的 NHW 部署地址
  minHealthyNodeRatio: 0.5
```

> 若未部署 [Node Health Watcher](https://github.com/Shaohan-He/node-health-watcher)，请将 `enabled: false` 或留空 `nhwEndpoint`。

### 7. 伸缩策略参数（按游戏调优）

**涉及文件：`config/samples/fleet-demo.yaml` 中的 `AutoscalerPolicy` 资源**

| 参数 | 你应设置为 | 含义 |
|------|-----------|------|
| `scalingMetric.targetValue` | 你的游戏服最大玩家数的 80% | 目标每服玩家数，低于上限留余量 |
| `auxiliaryMetrics` | CPU 权重 0.1~0.3 | 辅助指标加权，CPU > 70% 时追加服务器 |
| `buffer.size` | 高峰期每分钟分配请求数的 2~3 倍 | 暖池大小，防止冷启动延迟 |
| `drain.timeoutSeconds` | 你的最长对局时间 + 缓冲 | 排水时等待对局结束的最大秒数 |
| `minReplicas` / `maxReplicas` | 你的集群节点数 × 每节点最大服数 | 副本数边界 |
| `sessionQuery.periodSeconds` | 30s（与 drain.intervalSeconds 一致） | 排水期间查询游戏服活跃会话的间隔 |

> **完整 CRD 字段参考见下方 [CRD 设计](#crd-设计) 章节。**

---

## 功能模块

### Fleet Controller — 舰队协调引擎

核心控制循环：观察舰队期望状态 → 采集实际状态 → 计算差异 → 执行调谐。

```
每 15 秒 reconcile 一次。循环内执行：
  1. 采集指标（玩家数、CPU、内存、分配率）
  2. 评估伸缩策略（min/max/buffer/cool-down）
  3. 节点健康过滤（查询 NHW 端点，排除不健康节点）
  4. 计算 desired replicas，执行创建或排水
  5. 更新 Fleet Status（ready / allocated / draining / buffer）
```

**伸缩决策示例：**
```
舰队: "battle-royale-fleet"
├─ 当前副本: 12（已分配 8，暖池 3，排水中 1）
├─ 总玩家数: 640（每服上限 100，理论需要 7 台）
├─ Buffer 策略: 保持 3 台温备
├─ Desired: max(7+3, 12) = 12  → 无需变更
└─ 决策: HOLD（玩家数与 buffer 均在策略范围内）
```

**再示例：**
```
舰队: "battle-royale-fleet"
├─ 当前副本: 12（已分配 11，暖池 0，排水中 1）
├─ 总玩家数: 1050（每服上限 100，理论需要 11 台）
├─ Buffer 策略: 保持 3 台温备
├─ Desired: 11+3 = 14 → 需要扩容 2 台
├─ 节点过滤: node-3 NotReady（NHW 报告）→ 跳过
└─ 决策: SCALE_UP (+2)，调度至健康节点
```

### Allocation Controller — 分配编排器

处理来自匹配器的游戏服分配请求，实现"获取一台就绪的游戏服"的完整链路。

```
匹配器 POST /api/v1/allocate → Allocation CR 创建 → Controller reconcile:
  1. 按 fleetRef 定位目标舰队，从该舰队的暖池中选取就绪游戏服
  2. 按优选策略排序（最少玩家数 / 最低延迟 / 轮询 / 严格装箱）
  3. 标记游戏服为 Allocated，从暖池中移除
  4. 触发暖池补充
  5. 返回游戏服地址与端口给匹配器
```

**分配策略：**

| 策略 | 行为 | 适用场景 |
|------|------|---------|
| `FewestPlayers` | 选择当前玩家数最少的服 | 大多数场景 |
| `LowestLatency` | 选择到玩家平均延迟最低的服 | 竞技类游戏 |
| `RoundRobin` | 按顺序轮流分配 | 压力均匀分布 |
| `StrictBinPack` | 尽量填满一台再分配下一台 | 成本优先 |

### Graceful Drainer — 优雅排水

三阶段排水协议，确保不丢失任何一局正在进行的对局。

```
Phase 1: CORDON（拒新）
  └─ 游戏服状态 → Draining
  └─ 分配器不再将此服分配给新对局
  └─ 等待时间: 0s（立即生效）

Phase 2: DRAIN（排空）
  └─ 等待活跃对局自然结束
  └─ 定期查询游戏服 API: GET /sessions
  └─ 最大等待: drain_timeout（默认 10min）
  └─ 每 30s 检查一次活跃会话数

Phase 3: DECOMMISSION（回收）
  └─ 活跃会话 = 0 或超时
  └─ 发送 SIGTERM 给游戏服进程
  └─ terminationGracePeriodSeconds 后 SIGKILL
  └─ 更新 Fleet Status
```

```
[2026-05-24 14:30:00] [OK] [drainer] battle-royale-server-7 → CORDON（拒新）
[2026-05-24 14:33:30] [OK] [drainer] battle-royale-server-7 → 活跃对局: 2 → 1
[2026-05-24 14:36:00] [OK] [drainer] battle-royale-server-7 → 活跃对局: 0
[2026-05-24 14:36:05] [OK] [drainer] battle-royale-server-7 → DECOMMISSION（Pod 已回收）
```

### Buffer Pool — 暖池管理

维护指定数量的预热就绪游戏服，匹配器可即时分配，消除玩家等待冷启动的延迟。

```
暖池 = 已启动 + 健康检查通过 + 未分配的游戏服 Pod

行为:
  └─ 分配发生后 → 暖池 -1 → 异步触发补充
  └─ 暖池 < bufferSize → 创建新 Pod（经节点健康过滤）
  └─ 暖池 > bufferSize（如扩容回撤后）→ 标记多余服排空
  └─ 游戏服健康检查失败 → 自动剔除并补充
  └─ 游戏服空闲超时（idle_timeout）→ 自动排水回收
```

### Health-Aware Scaling — 节点健康感知

通过查询 Node Health Watcher 的 API 端点获取节点健康状态，在扩缩容决策中过滤不健康节点。

```
Controller reconcile 时:
  1. GET <nhw-endpoint>/api/v1/node-health
  2. 解析节点健康报告（WARNING / CRITICAL / HEALTHY）
  3. 构建健康节点列表
  4. 调度决策仅使用健康节点
  5. 若健康节点不足 → 降级告警 + 暂停扩容 + 发 IM 通知
```

```json
// Node Health Watcher 端点返回示例
{
  "timestamp": "2026-05-24T14:30:00Z",
  "nodes": {
    "node-1": {"status": "HEALTHY", "score": 95},
    "node-2": {"status": "HEALTHY", "score": 88},
    "node-3": {"status": "CRITICAL", "score": 12, "reason": "disk: /var = 94%"}
  }
}
```

### Scaling Event Notifier — 扩缩容事件推送

每次扩缩容决策执行后推送 IM 通知，与 Node Health Watcher 共用同一套告警通道。

```
🎮 Game Fleet 扩缩容通知 2026-05-24 14:30:00

📈 SCALE_UP (+2) [battle-royale-fleet]
├─ 原因: 玩家数 640 → 1050，暖池不足
├─ 当前副本: 12 → 目标副本: 14
├─ 节点: node-1, node-2

📉 SCALE_DOWN (-1) [lobby-fleet]
├─ 原因: 玩家数下降，buffer 超配
├─ 排水: lobby-server-3 (0 活跃会话)
└─ 当前副本: 5 → 目标副本: 4
```

## CRD 设计

### GameServerFleet

定义一支游戏服舰队——按游戏模式或区域划分的一组同构游戏服。

```yaml
apiVersion: director.gamefleet.io/v1alpha1
kind: GameServerFleet
metadata:
  name: battle-royale-fleet
  namespace: game-fleet-demo
spec:
  # 游戏服 Pod 模板（对齐 K8s PodTemplateSpec）
  template:
    spec:
      containers:
        - name: game-server
          image: registry.example.com/battle-royale:v1.2.3
          ports:
            - name: game
              containerPort: 7777
              protocol: UDP
            - name: metrics          # HTTP 端点端口（命名为 metrics 确保被正确发现）
              containerPort: 8080
              protocol: TCP
          env:
            - name: MAX_PLAYERS
              value: "100"
            - name: GAME_MODE
              value: "battle-royale"

  # 伸缩策略引用
  autoscalerRef:
    name: br-scaling-policy

  # 游戏服健康检查（覆盖 Pod 级别的 probe）
  healthCheck:
    httpGet:
      path: /healthz
      port: 8080
    periodSeconds: 10
    failureThreshold: 3

  # 会话查询端点（排水时查询活跃玩家数）
  sessionQuery:
    httpGet:
      path: /api/v1/sessions
      port: 8080

  # 节点亲和性（如 GPU 节点、低延迟机房）
  nodeSelector:
    topology.kubernetes.io/region: cn-beijing

status:
  replicas: 12
  readyReplicas: 12
  allocatedReplicas: 8
  drainingReplicas: 0
  bufferPool: 3
  totalPlayers: 640
  conditions:
    - type: Available
      status: "True"
      lastTransitionTime: "2026-05-24T14:00:00Z"
```

### AutoscalerPolicy

定义伸缩策略——与舰队解耦，允许多支舰队共享同一套策略。

```yaml
apiVersion: director.gamefleet.io/v1alpha1
kind: AutoscalerPolicy
metadata:
  name: br-scaling-policy
spec:
  # 副本数范围
  minReplicas: 3
  maxReplicas: 50

  # 暖池配置
  buffer:
    size: 3                     # 始终保持 3 台温备
    idleTimeout: 300s           # 温备服空闲超过 5 分钟自动回收

  # 主伸缩指标
  scalingMetric:
    type: PlayersPerServer      # 每服平均玩家数
    targetValue: 80             # 目标每服 80 人时触发扩容（上限 100，留 20 余量）

  # 辅助指标（加权）
  auxiliaryMetrics:
    - type: AllocationRate      # 分配请求 QPS
      weight: 0.3
    - type: CPU                 # CPU 使用率
      weight: 0.1

  # 冷却时间
  cooldown:
    scaleUp: 60s                # 扩容后 60s 内不再扩容
    scaleDown: 300s             # 缩容后 5min 内不再缩容

  # 排水策略
  drain:
    timeout: 600s               # 单台最大排水等待时间（10 分钟）
    interval: 30s               # 会话检查间隔
    forceAfter: 1800s           # 超过 30 分钟强制回收（异常保护）

  # 分配策略
  allocation:
    strategy: FewestPlayers     # 优先分配玩家最少的服

  # 熔断
  circuitBreaker:
    consecutiveFailures: 5      # 连续失败次数阈值
    cooldownPeriod: 300s        # 熔断冷却时间

  # 节点健康感知
  nodeHealth:
    enabled: true
    provider: nhw               # 节点健康数据来源
    nhwEndpoint: "http://node-health-watcher.nhw-system:8080"
    minHealthyNodeRatio: 0.5    # 健康节点比例低于 50% 时暂停扩容
```

### GameServerAllocation

匹配器发起的游戏服分配请求——声明式、可审计、有状态。

```yaml
apiVersion: director.gamefleet.io/v1alpha1
kind: GameServerAllocation
metadata:
  name: match-abc123
  namespace: game-fleet-demo
spec:
  # 目标舰队
  fleetRef:
    name: battle-royale-fleet

  # 所需游戏服标签（如特定地图、模式）
  selectors:
    matchLabels:
      game-mode: battle-royale
      map-pool: season-3

  # 分配策略（覆盖舰队默认策略）
  strategy: LowestLatency

  # 玩家元数据（用于延迟优选）
  players:
    regions: ["cn-beijing", "cn-shanghai"]
    preferredLatencyMs: 50

  # TTL：如果在 30s 内未分配成功，标记失败
  ttlSeconds: 30

status:
  phase: Allocated              # Pending → Allocated → Failed → Released
  gameServer:
    name: battle-royale-server-5
    endpoint: "10.0.3.15:7777"
    node: node-2
  playerCount: 23
  allocatedAt: "2026-05-24T14:30:00Z"
  sessionId: "match-abc123"
```

---

## 配置说明

### 部署配置（Makefile）

| 变量 | 默认值 | 说明 |
|------|-------|------|
| `REGISTRY` | `registry.cn-beijing.aliyuncs.com/noneedtostudy` | **部署前必改。** 镜像仓库地址，按你的云厂商和地域修改 |
| `TAG` | `v0.1.0` | 镜像版本标签 |
| `GOPROXY` | `https://goproxy.cn,direct` | Go 模块代理。国内用户保留此设置；海外用户可设为 `https://proxy.golang.org,direct` |

### Controller 配置

Controller 支持 CLI flag 和环境变量两种配置方式。环境变量（`GFD_*`）的优先级高于 CLI flag 默认值，便于在 Kubernetes Deployment 中注入。

| CLI flag | 环境变量 | 默认值 | 说明 |
|----------|---------|-------|------|
| `--metrics-addr` | `GFD_METRICS_ADDR` | `:8080` | Controller metrics / healthz 监听地址 |
| `--namespace` | `GFD_NAMESPACE` | `game-fleet-system` | Controller 部署命名空间 |
| `--resync-period` | `GFD_RESYNC_PERIOD` | `15s` | Fleet reconcile 间隔 |
| `--leader-elect` | `GFD_LEADER_ELECT` | `true` | 启用 Leader Election（多副本部署） |
| `--log-level` | `GFD_LOG_LEVEL` | `info` | 日志级别（debug/info/warn/error） |
| `--nhw-endpoint` | `GFD_NHW_ENDPOINT` | `""` | Node Health Watcher API 端点（空则禁用节点健康感知） |
| `--nhw-timeout` | `GFD_NHW_TIMEOUT` | `10s` | NHW 查询超时 |
| `--notify-feishu-url` | `GFD_NOTIFY_FEISHU_URL` | `""` | 飞书 Webhook URL（空则禁用飞书通知） |
| `--notify-dingtalk-url` | `GFD_NOTIFY_DINGTALK_URL` | `""` | 钉钉 Webhook URL（空则禁用钉钉通知） |

### API Server 配置

| CLI flag | 环境变量 | 默认值 | 说明 |
|----------|---------|-------|------|
| `--api-addr` | `GFD_API_ADDR` | `:8443` | Allocation API Server 监听地址 |
| `--metrics-addr` | `GFD_METRICS_ADDR` | `:8080` | API Server metrics / healthz 监听地址 |
| `--rate-limit` | `GFD_RATE_LIMIT` | `100` | 分配 API 每秒最大请求数 |
| `--tls-cert-file` | — | `""` | TLS 证书路径（与 `--tls-key-file` 同时设置启用 HTTPS） |
| `--tls-key-file` | — | `""` | TLS 私钥路径（与 `--tls-cert-file` 同时设置启用 HTTPS） |
| — (仅环境变量) | `GFD_LOG_LEVEL` | `info` | 日志级别（debug/info） |

### 游戏服契约（你的游戏服需实现的 HTTP 端点）

| 端点 | 方法 | 响应格式 | 用途 |
|------|------|---------|------|
| `:8080/healthz` | GET | `200 OK` | 暖池就绪检查、健康探针 |
| `:8080/api/v1/sessions` | GET | `{"activeSessions": 3}` | 排水协议查询活跃对局数 |
| `:8080/api/v1/metrics` | GET | `{"players": 47, "cpuPercent": 35.2, "memoryMB": 512.0}` | 伸缩指标采集 |

> **端口命名约定：** Game Fleet Director 通过 Pod 模板中的容器端口名称来发现 HTTP 端点。建议将游戏服的 HTTP 端口命名为 `metrics`（优先匹配）或 `http`，否则会使用首个 TCP 端口，最后回退到 8080。

### REST API（匹配器接口）

Game Fleet Director 的 API Server 提供 RESTful 接口供匹配器调用。

**POST /api/v1/allocate** — 分配一台游戏服

```json
// 请求体
{
  "fleet": "battle-royale-fleet",
  "namespace": "game-fleet-demo",
  "strategy": "FewestPlayers",
  "regions": ["cn-beijing", "cn-shanghai"],
  "ttlSeconds": 30
}
// 响应 (200 OK)
{
  "serverName": "battle-royale-server-5",
  "endpoint": "10.0.3.15:7777",
  "node": "node-2",
  "sessionId": "session-1716561000123456789"
}
// 无可用服 (503)
{ "error": "no available server: ..." }
```

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `fleet` | string | 是 | 目标舰队名称 |
| `namespace` | string | 否 | 舰队命名空间（默认 `default`） |
| `strategy` | string | 否 | 分配策略：`FewestPlayers`（默认）/ `LowestLatency` / `RoundRobin` / `StrictBinPack` |
| `regions` | []string | 否 | 玩家所在区域，用于延迟优选 |
| `ttlSeconds` | int32 | 否 | 分配请求 TTL，超时标记失败 |

**POST /api/v1/release** — 释放一台已分配的游戏服

```json
// 请求体
{
  "fleet": "battle-royale-fleet",
  "namespace": "game-fleet-demo",
  "serverName": "battle-royale-server-5"
}
// 响应 (200 OK)
{ "status": "released" }
```

**GET /api/v1/fleets/{namespace}/{name}/status** — 查询舰队暖池状态

```json
// 响应 (200 OK)
{
  "pool": {
    "total": 12,
    "healthy": 11,
    "unhealthy": 1,
    "allocated": 8,
    "bufferAvailable": 3
  }
}
```

### Prometheus Metrics

Controller 在 `:8080/metrics` 暴露以下 Prometheus 指标：

| 指标名 | 类型 | 标签 | 说明 |
|--------|------|------|------|
| `gfd_fleet_replicas` | Gauge | fleet, namespace | 当前副本数 |
| `gfd_fleet_players_total` | Gauge | fleet, namespace | 舰队总玩家数 |
| `gfd_fleet_buffer_available` | Gauge | fleet, namespace | 暖池可用数 |
| `gfd_scale_up_total` | Counter | fleet, namespace | 扩容次数 |
| `gfd_scale_down_total` | Counter | fleet, namespace | 缩容次数 |
| `gfd_allocation_total` | Counter | fleet, strategy, phase | 分配请求计数与结果 |
| `gfd_allocation_latency_seconds` | Histogram | fleet, strategy | 分配延迟分布 |
| `gfd_drain_duration_seconds` | Histogram | fleet | 排水耗时分布 |
| `gfd_reconcile_duration_seconds` | Histogram | controller | Reconcile 耗时 |
| `gfd_circuit_breaker_triggered` | Counter | fleet | 熔断触发次数 |

---

## 开发

```bash
# 克隆仓库
git clone https://github.com/Shaohan-He/game-server-orchestrator.git
cd game-server-orchestrator

# 安装依赖
go mod download

# 生成 CRD 代码（deepcopy、client、informer、lister）
make generate

# 构建
make build

# 运行单元测试
make test

# 运行集成测试（需要 envtest 二进制）
make test-integration

# 运行端到端测试（需要 kind 集群）
make test-e2e

# 代码检查
make lint          # golangci-lint run
make vet           # go vet ./...

# 构建 Docker 镜像
make docker-build TAG=v0.1.0

# 部署到当前 kubectl context 所在集群
make deploy TAG=v0.1.0

# 安装 CRD
make install-crds
```

### 本地开发

```bash
# 在本地运行 Controller（需要 kubeconfig 指向开发集群）
make run-controller

# 在本地运行 API Server
make run-apiserver

# 创建 kind 开发集群并部署全套组件
make dev-up
make dev-down
```

### 编写 E2E 测试

E2E 测试通过 kubeconfig 或 envtest 连接到 K8s 集群，验证完整的 Fleet 生命周期和 REST API 行为：

- **TestFleetLifecycle**: 创建 Fleet + Policy，等待 Controller 产出 GameServer Pod，验证 fleet status 字段（replicas / bufferPool）
- **TestAllocationAPI**: 调用 API Server 的 `/api/v1/allocate` 端点，验证分配返回 serverName 和 endpoint
- **TestHealthEndpoint**: 验证 `/healthz` 健康检查端点
- **TestFleetStatusEndpoint**: 验证 `/api/v1/fleets/{ns}/{name}/status` 查询端点

运行方式：
```bash
# 通过 kind 部署完整环境后运行
make dev-up
make test-e2e
```

---

## 贡献

1. Fork 本仓库
2. 创建功能分支：`git checkout -b feat/my-feature`
3. 确保 `make lint && make test` 在本地通过
4. 向 `main` 分支发起 Pull Request

所有 PR 将通过 GitHub Actions 自动执行 golangci-lint 静态检查与单元测试。

---

## 许可证

MIT © 2026 [Shaohan He](https://github.com/Shaohan-He)

---

## English

## Overview

**Game Fleet Director** is a Kubernetes-native fleet lifecycle manager purpose-built for game servers. It implements an Operator pattern with player-aware autoscaling, graceful draining, and warm buffer-pool allocation — bridging the semantic gap between generic autoscalers (HPA/VPA) and stateful game workloads.

### Why Game Fleet Director?

[node-health-watcher](https://github.com/Shaohan-He/node-health-watcher) answers "when to act" — it pushes IM alerts when nodes misbehave. [node-guardian](https://github.com/Shaohan-He/node-guardian) answers "how to fix it" — it provides a diagnostic and hardening toolchain when you SSH into a broken node.

But neither answers **"who operates the game servers themselves"**:

- You can't let HPA decide based on CPU — a full-lobby server at 40% CPU must be protected; an idle server at 30% can be reclaimed
- You can't just `kubectl delete pod` — there are 47 players in active matches
- Your matchmaker needs a warm game server instantly, not 30 seconds after Pod creation
- Your scaling decisions need node-health context — don't schedule onto a NotReady node

**Game Fleet Director fills this final gap:** it understands game server semantics — matches, player sessions, buffer pools, drain timeouts — and encodes them as K8s-native declarative CRDs and Controllers.

### The Trilogy

| Project | Language | Answers the Question | Ops Phase |
|---------|----------|---------------------|-----------|
| **Node Guardian** | Bash | How do I diagnose and fix a broken node? | Respond |
| **Node Health Watcher** | Python | When should I go check on things? | Detect |
| **Game Fleet Director** | Go | Who operates the game servers themselves? | Act |

### Core Principles

| Principle | Implementation |
|-----------|---------------|
| **Player-Aware Scaling** | Primary scaling metric = player count; CPU/memory as auxiliary signals only |
| **Graceful Draining** | Three-phase drain protocol: Cordon → Drain (wait for matches) → Decommission; never kill a pod with active players |
| **Buffer Pool** | Maintain N pre-warmed game servers ready for instant allocation; eliminates cold-start latency |
| **Declarative Management** | CRD-based fleet, policy, and allocation definitions; GitOps-ready |
| **Node-Health Aware** | Integrates with Node Health Watcher API; filters unhealthy nodes from scheduling decisions |
| **Defensive Control Loop** | Dry-run mode, rate limiting, circuit breaker; single reconcile failure never blocks the next cycle |

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/Shaohan-He/game-server-orchestrator.git
cd game-server-orchestrator

# 2. Download Go dependencies
go mod tidy

# 3. Build binaries
make build

# 4. Edit Makefile line 5 — replace REGISTRY with your own container registry
#    REGISTRY ?= registry.cn-beijing.aliyuncs.com/your-namespace

# 5. Build & push Docker images
make docker-build TAG=v0.1.0
make docker-push  TAG=v0.1.0

# 6. Edit deploy/kustomization.yaml — update image references to your REGISTRY

# 7. If using a private registry, create imagePullSecrets
kubectl create namespace game-fleet-system
kubectl create secret docker-registry acr-secret \
  --docker-server=your-registry.example.com \
  --docker-username=<your-username> \
  --docker-password=<your-password> \
  -n game-fleet-system

# 8. Install CRDs
kubectl apply -f config/crds/

# 9. Deploy Controller + API Server
kubectl apply -k deploy/

# 10. Edit config/samples/fleet-demo.yaml — replace image with your game server image
#     Then deploy a sample fleet
kubectl create namespace game-fleet-demo
kubectl apply -f config/samples/fleet-demo.yaml

# 11. Check fleet status
kubectl get gameserverfleets -n game-fleet-demo
kubectl describe gameserverfleet battle-royale-fleet -n game-fleet-demo

# 12. Inspect buffer pool
kubectl get gameserverfleet battle-royale-fleet -o jsonpath='{.status.bufferPool}'

# 13. Simulate matchmaker allocating a game server
kubectl apply -f config/samples/allocation-request.yaml

# 14. Dry-run mode — preview scaling decisions
kubectl annotate gameserverfleet battle-royale-fleet \
  director.gamefleet.io/dry-run="true" --overwrite

# 15. Watch Controller logs
kubectl logs -n game-fleet-system deploy/game-fleet-director-controller -f
```

### Prerequisites

- **Go 1.22+** (build only)
- **Kubernetes 1.28+** (CRD + controller-runtime dependency)
- **Container image registry** (Alibaba Cloud ACR / Tencent Cloud TCR / Huawei Cloud SWR / self-hosted Harbor)
- **Prometheus** (optional, for custom metrics collection & HPA coordination)
- **Node Health Watcher** (optional, for node-health-aware scaling)
- Review the sample manifests before applying them to a production cluster.

---

## Required Pre-Deploy Configuration

The following items use **placeholder values** in the repository. **You must change them** before deploying to any real cluster. See the [Chinese section above](#部署前必改配置) for detailed step-by-step instructions.

| # | What to change | Where | Why |
|---|---------------|-------|-----|
| 1 | **Container registry URL** | `Makefile` L5, `deploy/controller.yaml`, `deploy/apiserver.yaml`, `deploy/kustomization.yaml` | Default points to a non-existent ACR namespace |
| 2 | **Go module proxy** | `Makefile` L9 (`GOPROXY`) | Developers outside China should remove or change `goproxy.cn` |
| 3 | **Image pull secret** | `deploy/controller.yaml` → uncomment `imagePullSecrets` | Required for private registries |
| 4 | **Game server image** | `config/samples/fleet-demo.yaml` → `.spec.template.spec.containers[0].image` | Placeholder image will never pull successfully |
| 5 | **IM webhook URLs** | Controller `--notify-feishu-url` / `--notify-dingtalk-url` flags | Empty by default (notifications disabled); set to receive scaling alerts |
| 6 | **NHW endpoint** | `AutoscalerPolicy` CR → `.spec.nodeHealth.nhwEndpoint` | Must point to your actual Node Health Watcher deployment, or disable with `enabled: false` |
| 7 | **Scaling parameters** | `AutoscalerPolicy` CR → `scalingMetric.targetValue`, `buffer.size`, `drain.timeoutSeconds`, `minReplicas`/`maxReplicas` | Must match your game server's player capacity, match duration, and cluster size |

### Game Server Contract (endpoints your game server must expose)

| Endpoint | Method | Response | Purpose |
|----------|--------|----------|---------|
| `:8080/healthz` | GET | `200 OK` | Buffer pool readiness probe |
| `:8080/api/v1/sessions` | GET | `{"activeSessions": 3}` | Drain protocol — query active match count |
| `:8080/api/v1/metrics` | GET | `{"players": 47, "cpuPercent": 35.2, "memoryMB": 512.0}` | Scaling metric collection |

---

### Fleet Controller — Fleet Reconciliation Engine

Core control loop: observe desired fleet state → collect actual state → compute delta → execute reconciliation.

```
Reconciles every 15s. Each cycle:
  1. Collect metrics (players, CPU, memory, allocation rate)
  2. Evaluate scaling policy (min/max/buffer/cooldown)
  3. Node health filtering (query NHW endpoint, exclude unhealthy nodes)
  4. Compute desired replicas; create new pods or initiate draining
  5. Update Fleet Status (ready / allocated / draining / buffer)
```

### Allocation Controller — Allocation Orchestrator

Handles allocation requests from the matchmaker. Implements the full "get me a ready game server" flow.

```
Matchmaker POST /api/v1/allocate → Allocation CR created → Controller reconcile:
  1. Locate the target fleet by fleetRef, pick a ready server from its buffer pool
  2. Rank by preferred strategy (FewestPlayers / LowestLatency / RoundRobin / StrictBinPack)
  3. Mark game server as Allocated, remove from buffer pool
  4. Trigger buffer pool refill
  5. Return game server address & port to matchmaker
```

**Allocation Strategies:**

| Strategy | Behavior | Best For |
|----------|----------|----------|
| `FewestPlayers` | Pick the server with the fewest current players | Most use cases |
| `LowestLatency` | Pick the server with lowest average latency to players | Competitive games |
| `RoundRobin` | Rotate sequentially through available servers | Even load distribution |
| `StrictBinPack` | Fill one server to capacity before using the next | Cost optimization |

### Graceful Drainer — Three-Phase Drain Protocol

Ensures no active match is ever lost during scale-down.

```
Phase 1: CORDON
  └─ GameServer state → Draining
  └─ Allocator stops assigning this server to new matches
  └─ Wait time: 0s (immediate)

Phase 2: DRAIN
  └─ Wait for active matches to finish naturally
  └─ Periodically query game server API: GET /sessions
  └─ Max wait: drain_timeout (default 10min)
  └─ Check active session count every 30s

Phase 3: DECOMMISSION
  └─ Active sessions = 0 or timeout reached
  └─ Send SIGTERM to game server process
  └─ SIGKILL after terminationGracePeriodSeconds
  └─ Update Fleet Status
```

### Buffer Pool — Warm Server Pool

Maintains a configurable number of pre-warmed, health-checked, unallocated game servers for instant allocation.

```
Buffer = started + health-check passing + unallocated game server Pods

Behavior:
  └─ Allocation occurs → buffer -1 → async refill triggered
  └─ Buffer < bufferSize → create new Pods (via node health filter)
  └─ Buffer > bufferSize (e.g. after scale-down) → mark excess for draining
  └─ Game server health check fails → auto-evict from pool and refill
  └─ Game server idle timeout → auto-drain and reclaim
```

### Health-Aware Scaling

Queries the Node Health Watcher API to obtain node health status and filters unhealthy nodes from scaling decisions.

```
During Controller reconcile:
  1. GET <nhw-endpoint>/api/v1/node-health
  2. Parse node health report (WARNING / CRITICAL / HEALTHY)
  3. Build healthy node list
  4. Schedule only onto healthy nodes
  5. If healthy node ratio drops below threshold → degraded alert + pause scale-up + IM notification
```

### Scaling Event Notifier

Pushes IM notifications after each scaling decision, sharing the same alerting channels as Node Health Watcher.

```
🎮 Game Fleet Scaling Event 2026-05-24 14:30:00

📈 SCALE_UP (+2) [battle-royale-fleet]
├─ Reason: players 640 → 1050, buffer pool depleted
├─ Replicas: 12 → 14
├─ Nodes: node-1, node-2

📉 SCALE_DOWN (-1) [lobby-fleet]
├─ Reason: player count drop, buffer over-provisioned
├─ Draining: lobby-server-3 (0 active sessions)
└─ Replicas: 5 → 4
```

## Architecture

```
.
├── cmd/
│   ├── controller/                # K8s Controller entry point (main.go)
│   └── apiserver/                 # REST API Server entry point (matchmaker interface)
├── api/
│   └── v1alpha1/                  # CRD type definitions
│       ├── gameserverfleet_types.go       # Fleet CRD
│       ├── autoscalerpolicy_types.go      # Scaling policy CRD
│       └── gameserverallocation_types.go  # Allocation request CRD
├── pkg/
│   ├── controller/                # Reconciler implementations
│   │   ├── fleet_controller.go    # Fleet reconciliation loop
│   │   ├── alloc_controller.go    # Allocation reconciliation loop
│   │   ├── scaler.go              # Scaling decision engine
│   ├── drainer/                   # Graceful draining
│   │   ├── drainer.go             # Three-phase drain protocol
│   │   └── session_tracker.go     # Active session tracking
│   ├── pool/                      # Buffer pool management
│   │   ├── pool.go                # Buffer refill & reclamation
│   │   ├── scheduler.go           # Game server preference scheduler (multi-strategy ranking)
│   │   └── warmer.go              # Game server pre-warming
│   ├── health/                    # Health check integration
│   │   ├── nhw_client.go          # Node Health Watcher API client
│   │   └── node_filter.go         # Node health filter
│   ├── metrics/                   # Metrics collection
│   │   ├── collector.go           # Prometheus metrics collector
│   │   └── scraper.go             # Game server metrics scraper
│   ├── api/                       # REST API
│   │   ├── server.go              # HTTP server initialization
│   │   ├── handler.go             # Allocate / release / query endpoints
│   │   └── middleware.go          # Auth & rate-limiting middleware
│   └── notifier/                  # IM notifications
│       ├── notifier.go            # Notification facade (Feishu/DingTalk/WeCom)
│       └── template.go            # Message template renderer
├── config/
│   ├── crds/                      # CRD YAML manifests
│   ├── samples/                   # Example custom resources
│   └── rbac/                      # RBAC (Controller permissions)
├── deploy/
│   ├── controller.yaml
│   ├── apiserver.yaml
│   └── kustomization.yaml
├── tests/
│   ├── e2e/                       # End-to-end tests (kind cluster + REST API validation)
│   └── integration/               # Integration tests (envtest + CRD DeepCopy + Scheme registration)
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
└── README.md
```

### Design Decisions

**Why Go instead of continuing with Python?**

The K8s Operator ecosystem is Go's native habitat. controller-runtime, client-go, and controller-gen (code generator) are all Go libraries. Writing an Operator in Python (kopf) works, but lacks type safety, has a smaller community, and fewer production references. Game infrastructure (Agones, Nakama, GKE Game Servers) is also entirely Go — this is the industry standard.

More concretely: CRD type definitions require strong typing → JSON schema auto-generation → deepcopy code generation. Go's `controller-gen` does this at compile time; Python can only reflect at runtime, making debugging significantly more expensive.

**Why the Operator pattern instead of an external scheduler?**

| Dimension | K8s Operator | External Scheduler |
|-----------|-------------|-------------------|
| **State storage** | K8s etcd (zero extra dependency) | Requires separate DB |
| **Permission model** | RBAC, native | Must build custom |
| **Declarative semantics** | CRD kubectl apply → reconcile | Must build custom DSL |
| **High availability** | Deployment + Leader Election | Must build custom |
| **Observability** | Prometheus metrics, standard | Must build custom |
| **Community familiarity** | Pattern every interviewer knows | Requires explanation |

**Why player count as the primary scaling metric instead of CPU/memory?**

Game server load characteristics are fundamentally different from web services:

- A full-lobby server (100 players, 40% CPU) **must be protected**
- An idle matchmaking server (0 players, 30% CPU) **can be reclaimed**
- CPU cannot distinguish these two states — 30% vs 40% is nearly identical statistically

Player count is the game server's "first-principle metric" — it directly maps to business value. CPU/memory, allocation rate, and session duration serve as auxiliary dimensions to prevent anomalies (e.g. a server where players are doing physics-heavy computations causing CPU spikes).

**Why a three-phase drain protocol?**

The separation of CORDON (Phase 1) and DRAIN (Phase 2) is critical. If we sent SIGTERM directly, the game server's `terminationGracePeriodSeconds` (typically 30s) would never cover a 20-minute match. The three-phase separation means:

- CORDON takes effect immediately (0s) → no new players enter
- DRAIN gets its own independent timeout (`drain_timeout`, default 10min) → enough time for matches to end naturally
- DECOMMISSION reuses K8s native Pod termination → no game server changes needed

**Buffer Pool vs HPA?**

HPA is reactive — it scales up after metrics rise, and players wait through Pod cold starts. The buffer pool is anticipatory — it always maintains N ready servers for instant allocation. They can coexist: HPA adjusts `bufferSize` based on overall player trends; the buffer pool handles instant response.

**Why support running independently of Node Health Watcher?**

Every ops team has a different toolchain. Game Fleet Director's node-health awareness uses a pluggable `HealthProvider` interface. The default `NHWHealthProvider` calls the NHW API; a `StaticHealthProvider` reads from a ConfigMap. You can implement your own provider to integrate with your company's CMDB or monitoring system.

**Dry-run mode**

Adding the `director.gamefleet.io/dry-run: "true"` annotation to a Fleet CR causes the Controller to execute the full reconcile cycle (metric collection → policy evaluation → decision computation) but skip actual Pod creation/deletion. Decision logs are prefixed with `[DRY-RUN]` for auditing and policy debugging.

**Circuit breaker**

After N consecutive reconcile failures (default 5) → Controller pauses scaling operations for that Fleet for 5 minutes → pushes IM alert → waits for manual intervention. Prevents faulty metrics (e.g. scraper bug reporting 0 players) from triggering catastrophic scale-down.

## CRD Design

### GameServerFleet

Defines a fleet of homogeneous game servers — grouped by game mode or region.

### AutoscalerPolicy

Defines a scaling policy — decoupled from fleets so multiple fleets can share the same policy.

### GameServerAllocation

A matchmaker-issued game server allocation request — declarative, auditable, stateful.

(See the Chinese sections above for full CRD YAML examples.)

## Configuration

### Build Configuration (Makefile)

| Variable | Default | Description |
|----------|---------|-------------|
| `REGISTRY` | `registry.cn-beijing.aliyuncs.com/noneedtostudy` | **Must change before deploy.** Your container registry address. |
| `TAG` | `v0.1.0` | Image version tag. |
| `GOPROXY` | `https://goproxy.cn,direct` | Go module proxy. Developers outside China should use `https://proxy.golang.org,direct`. |

### Controller Configuration

Both CLI flags and environment variables are supported. `GFD_*` environment variables take precedence over CLI flag defaults.

| CLI flag | Env Variable | Default | Description |
|----------|-------------|---------|-------------|
| `--metrics-addr` | `GFD_METRICS_ADDR` | `:8080` | Controller metrics / healthz listen address |
| `--namespace` | `GFD_NAMESPACE` | `game-fleet-system` | Controller deployment namespace |
| `--resync-period` | `GFD_RESYNC_PERIOD` | `15s` | Fleet reconcile interval |
| `--leader-elect` | `GFD_LEADER_ELECT` | `true` | Enable Leader Election (multi-replica) |
| `--log-level` | `GFD_LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `--nhw-endpoint` | `GFD_NHW_ENDPOINT` | `""` | Node Health Watcher API endpoint (empty = disabled) |
| `--nhw-timeout` | `GFD_NHW_TIMEOUT` | `10s` | NHW query timeout |
| `--notify-feishu-url` | `GFD_NOTIFY_FEISHU_URL` | `""` | Feishu webhook URL (empty = disabled) |
| `--notify-dingtalk-url` | `GFD_NOTIFY_DINGTALK_URL` | `""` | DingTalk webhook URL (empty = disabled) |

### API Server Configuration

| CLI flag | Env Variable | Default | Description |
|----------|-------------|---------|-------------|
| `--api-addr` | `GFD_API_ADDR` | `:8443` | Allocation API Server listen address |
| `--metrics-addr` | `GFD_METRICS_ADDR` | `:8080` | API Server metrics / healthz listen address |
| `--rate-limit` | `GFD_RATE_LIMIT` | `100` | Max allocation requests per second |
| `--tls-cert-file` | — | `""` | TLS certificate path (set with `--tls-key-file` to enable HTTPS) |
| `--tls-key-file` | — | `""` | TLS private key path (set with `--tls-cert-file` to enable HTTPS) |
| — (env only) | `GFD_LOG_LEVEL` | `info` | Log level (debug/info) |

### Game Server Contract (endpoints your game server must implement)

| Endpoint | Method | Response | Purpose |
|----------|--------|----------|---------|
| `:8080/healthz` | GET | `200 OK` | Buffer pool readiness, liveness/readiness probe |
| `:8080/api/v1/sessions` | GET | `{"activeSessions": 3}` | Drain protocol active session query |
| `:8080/api/v1/metrics` | GET | `{"players": 47, "cpuPercent": 35.2, "memoryMB": 512.0}` | Scaling metric collection |

> **Port naming:** Game Fleet Director discovers HTTP endpoints by container port name. Name your game server's HTTP port `metrics` (preferred) or `http`; otherwise the first TCP port is used, falling back to 8080.

### Prometheus Metrics

Exposed at `:8080/metrics`:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `gfd_fleet_replicas` | Gauge | fleet, namespace | Current replica count |
| `gfd_fleet_players_total` | Gauge | fleet, namespace | Total players across fleet |
| `gfd_fleet_buffer_available` | Gauge | fleet, namespace | Available buffer pool size |
| `gfd_scale_up_total` | Counter | fleet, namespace | Scale-up event count |
| `gfd_scale_down_total` | Counter | fleet, namespace | Scale-down event count |
| `gfd_allocation_total` | Counter | fleet, strategy, phase | Allocation request count & result |
| `gfd_allocation_latency_seconds` | Histogram | fleet, strategy | Allocation latency distribution |
| `gfd_drain_duration_seconds` | Histogram | fleet | Drain duration distribution |
| `gfd_reconcile_duration_seconds` | Histogram | controller | Reconcile loop duration |
| `gfd_circuit_breaker_triggered` | Counter | fleet | Circuit breaker activation count |

## Development

```bash
git clone https://github.com/Shaohan-He/game-server-orchestrator.git
cd game-server-orchestrator

go mod download

# Generate CRD code (deepcopy, client, informer, lister)
make generate

# Build
make build

# Unit tests
make test

# Integration tests (requires envtest binary)
make test-integration

# End-to-end tests (requires kind cluster)
make test-e2e

# Lint
make lint          # golangci-lint run
make vet           # go vet ./...

# Docker build
make docker-build TAG=v0.1.0

# Deploy to current kubectl context
make deploy TAG=v0.1.0

# Install CRDs
make install-crds
```

### Local Development

```bash
# Run Controller locally (requires kubeconfig pointing to dev cluster)
make run-controller

# Run API Server locally
make run-apiserver

# Spin up a kind dev cluster with full stack
make dev-up
make dev-down
```

### Writing E2E Tests

E2E tests use [kind](https://kind.sigs.k8s.io/) to create ephemeral clusters, deploy the Controller and a sample game server image, and validate the full lifecycle:

```go
func TestFleetScaleUpOnPlayerIncrease(t *testing.T) {
    // 1. Create Fleet (min=2, buffer=1)
    fleet := newFleet("scale-test", 2, 1)
    k8sClient.Create(ctx, fleet)

    // 2. Wait for initial replicas to be ready
    waitForReadyReplicas(t, k8sClient, "scale-test", 3) // 2 + buffer 1

    // 3. Simulate player influx (inject fake data via game server metrics endpoint)
    simulatePlayers(t, "scale-test", 250) // needs 4 servers (250/80+1 + buffer 1)

    // 4. Assert scale-up occurred
    waitForReadyReplicas(t, k8sClient, "scale-test", 6) // 4 + buffer 2
}
```

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Ensure `make lint && make test` passes locally
4. Open a pull request against `main`

All PRs are automatically linted and tested via GitHub Actions.

## License

MIT © 2026 [Shaohan He](https://github.com/Shaohan-He)
