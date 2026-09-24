# 配置治理平台（Config Governance Center）

集中管理多环境、多租户微服务配置的平台。配置按 **命名空间 → 分组 → 配置项** 三级组织，
支持 JSON / YAML / Properties / TOML 四种格式与 JSON Schema 约束；后端统一计算
**公共层 → 命名空间层 → 分组层** 的三级深度合并并给出逐字段来源；每次改动自动留版本、
可一键回退、可任意两版本行级对比；变更通过 **长轮询（≤30s 挂起）** 与 **WebSocket**
实时下发，并支持 **按 IP 名单 / 按百分比灰度 → 观察 → 全量或灰度回退**。

> 范围严格限定在「配置治理」：不含账号、角色、权限等后台管理。租户隔离通过
> `X-Tenant-ID` 与后端全部按租户作用域的查询实现。

---

## 1. 一键运行

需要 Docker 24+（带 Compose v2）。

```bash
docker compose up -d --build
```

| 组件      | 地址                                   | 说明                    |
| --------- | -------------------------------------- | ----------------------- |
| 控制台    | http://localhost:8000                  | React 18 + Vite（nginx 托管并反代 API/WS） |
| 后端接口  | http://localhost:8080                  | Go 1.22 + Gin           |
| PostgreSQL | localhost:5432 （库 `configdb`，账号 config/config） | 配置与版本持久化 |
| Redis 7   | localhost:6379                         | 跨节点事件广播与下发计数 |

启动后自动建表并写入一个演示租户 `demo`（含命名空间 `payment`、分组 `gateway`
与一份三层示例配置 `app`）。打开控制台，左上角租户选「演示租户（demo）」即可看到
合并结果与逐字段来源。

健康检查：`GET http://localhost:8080/healthz`

### 不依赖外部服务的本地开发模式

后端可脱离 Postgres/Redis 运行（内存存储 + 进程内事件总线，仅用于开发/测试）：

```bash
cd backend
MEMORY_STORE=true USE_REDIS=false go run ./cmd/server
# 前端
cd frontend && npm install && npm run dev   # http://localhost:5173 ，已配置 /api 代理到 :8080
```

长轮询挂起时长可用 `LONG_POLL_TIMEOUT_SECONDS` 调整（生产固定 30s）。

---

## 2. 目录结构

```
backend/
  cmd/server/            程序入口：装配 Postgres/Redis、消费事件、建表、种子数据
  internal/
    domain/              领域模型（租户/命名空间/分组/配置项/版本/灰度发布/推送事件）
    validator/           ① 四种格式语法校验 + JSON Schema 校验（带行列/字段/约束）
    merge/               ② 三级合并内核：JSON/YAML 深度合并、Properties/TOML 键级覆盖、逐叶来源
    version/             ③ 版本间行级 diff（LCS，折叠出 change）
    gray/                ④ 灰度选择：IP 名单 + FNV 确定性百分比（可测、稳定）
    ref/                 ⑤ 占位符词法/语法 + 结构化掩码（引号感知哨兵）
    graph/               ⑥ 依赖图算法：迭代 DFS 环检测、反向传递闭包
    push/                ⑦ 长轮询挂起 / WebSocket 推送：Hub、事件总线（内存/Redis）、下发计数
    store/               持久化契约 + 内存实现（测试/参照）+ PostgreSQL 16 实现
    service/             业务编排：配额、校验、版本提交/回退、灰度生命周期、生效值与下发门禁
    api/                 Gin 路由与处理器、长轮询、WebSocket 升级
  internal/**/*_test.go  全部自动化测试
deploy/db/001_init.sql   PostgreSQL 建表脚本（容器启动时执行）
frontend/
  src/api/client.ts      类型化接口客户端
  src/components/
    TreeNav.tsx          命名空间→分组→配置项 树形导航
    ConfigEditor.tsx     按格式高亮的编辑区 + 灰度选项（IP/百分比）
    VersionTimeline.tsx  版本历史时间线 + 任意两版本 diff + 一键回退
    GrayPanel.tsx        灰度面板：已下发/总数、开始时间、持续时长、全量/回退
    Dashboard.tsx        各命名空间连接数、最近推送、在线实例看板
    EffectiveView.tsx    合并生效值 + 每个叶子字段的来源层标注
docker-compose.yml       四服务一键编排
```

---

## 3. 核心语义

### 3.1 三级合并与来源溯源

某个分组（服务）在某环境下看到的每个 Key，由三个容器叠加得到：

1. **公共层**：租户级 `_public / _public`，所有服务共享；
2. **命名空间层**：`<ns> / _defaults`，该业务线所有服务共享；
3. **分组层**：`<ns> / <group>`，该服务独有；下层同名键覆盖上层。

- JSON、YAML：嵌套对象 **逐字段深度合并**，不是整块替换。改上层某个未被下层覆盖的
  嵌套字段会生效，已被覆盖的字段不受影响。
- Properties、TOML：**按键级覆盖**（TOML 的整表随顶层键整体胜出）。
- 输出里每个叶子字段都带 `source`（public/namespace/group）与来源版本；
  `GET /api/effective` 与控制台「合并结果」页逐字段展示。

### 3.2 校验

保存前按 Key 的格式做语法校验，**不合法直接拒绝、不落库**，错误带行/列与原因；
若配置项挂了 JSON Schema，再做 Schema 校验，逐个字段点名报告
（如 `age` 违反 `minimum`、`name` 违反 `minLength`、缺少必填字段等）。

### 3.3 版本与回退

- 每个「配置项 + 环境」一条独立的单调递增版本号，乐观锁 `expected_version` 防并发覆盖。
- 版本记录操作者、时间、变更类型（create/update/rollback/gray_rollback）与全文值。
- 回退 = 把历史版本的值 **重新提交为一条新版本**，中间历史一条不删。
- 任意两版本可做行级 diff，标注新增/删除/修改。
- 版本保留数按租户配额裁剪（`version_retention`）。

### 3.4 实时下发

- **长轮询** `GET /api/poll?namespace=&group=&env=&version=&instance_id=&ip=`
  - 客户端带当前快照版本；服务端有更新立即返回最新合并快照；
  - 无更新则挂起至多 30 秒，期间变更立即唤醒；超时返回
    `{"changed":false,"message":"timeout, please reconnect"}`。
- **WebSocket** `GET /api/ws?namespace=&group=&env=`
  - 连接后先推一帧 `{"type":"snapshot",...}`，之后每次变更推 `change`/`promote`/
    `gray_rollback` 事件。
- 多实例后端时，变更经 **Redis Pub/Sub** 广播到各节点，再唤醒本节点连接。

### 3.5 灰度

保存新版本时带 `gray` 即开启灰度，生成一条 release（gray 状态）：

- `{"strategy":"ip","ips":["10.0.0.1"]}`：只对名单内实例下发；
- `{"strategy":"percent","percent":30}`：对实例 ID 与 release ID 做 FNV-1a 哈希取桶，
  桶号 `< percent` 者入选 —— **确定性**（同一实例在整个灰度期结论稳定）、比例可测。

灰度期间：

- 未入选实例经门禁（Gate）计算时读到的是灰度前的旧版本值，推送也不会唤醒它们；
- 控制台灰度面板实时显示「已下发实例数 / 连接总数」、开始时间与持续时长；
- **全量推送**（promote）后所有实例拿新版本；
- **灰度回退**：把旧值作为新版本（`gray_rollback`）写回，且只唤醒真正收到过灰度值
  的实例把它们拉回来；未入灰度的实例自始至终没离开旧值。

### 3.7 跨配置引用（占位符解引用）

一个键的值里可以写占位符，显式引用同租户下另一个键的**最终生效值**。占位符在
三级合并之后解引用，因此引用看到的永远是对方「合并 + 解引用完」的结果，引用可以链式传递。

**语法**（与普通文本严格区分，普通花括号、`$`、`@`、邮箱都不会被误判）：

| 写法 | 含义 |
| ---- | ---- |
| `@{key}` | 裸键名：按当前解析语境（公共层→命名空间层→分组层，强层优先）找该键 |
| `@{ns/group/key}` | 全限定：跨命名空间/分组定位一个键 |
| `@{key?env=prod}` | 指定读取对方在另一个环境的值 |
| `@@{` | 转义：写一个字面意义的 `@{` 定界符 |

占位符可嵌在字符串中间、一个字符串里可有多个、也可整段就是一个引用。JSON/YAML/TOML
里未加引号的整段占位符（结构位置）会代入对方的值或对象；引号内或 Properties 值里的
占位符按文本/标量代入，解引用后仍是各自格式的合法文档（解析前由 `ref.Mask` 做引号感知的
哨兵掩码，使 `{"timeout": @{base}}` 这类文本也能通过提交期校验）。

- **来源不断链**：`GET /api/effective` 每个叶子字段除原有 `source/source_version` 外，
  带 `resolved_fields[].value_refs`，标出每个被填入片段最终取自哪个键、那一层及版本，
  `through` 给出 A→B→C 的完整引用链。
- **解引用在灰度选版之后**：先决定这个实例该看到每层的哪个版本（命中灰度看新版、
  未命中看旧版），再在该版本结果上解引用。被引用键灰度时，未命中实例连同其依赖链上的
  键一起看到旧的解引用值。
- **生效版本随引用推进**：每个租户有一条单调版本时钟（revision），一个键的生效版本是
  它自身与整条引用闭包上 revision 的最大值。改了被引用键，所有（直接/间接）依赖它的键
  生效版本随之前进，长轮询与 WebSocket 照常被唤醒（按反向传递闭包扇出事件；灰度事件对
  整条链带同一 release id，由 Gate 用同一套实例判定把关）。

**依赖图与写入门禁**：平台长期维护 `item_refs` 谁引用谁（可正查「引了谁」、反查
「被谁依赖」）。提交新值时：占位符指向不存在目标 → 422 `ref_missing_target`；会成环
（含自引用）→ 422 `ref_cycle` 并按顺序给出完整闭环；目标键在该环境无值 → 读取时
422/错误 `ref_no_value`，绝不把占位符原样留在结果里。引用只在同一租户内成立，跨租户
坐标解析不到目标，在提交时即被拒。

改动前的爆炸半径：`GET /api/items/:id/impact?env=` 返回 `direct`（直接依赖）与
`transitive`（沿反向边的传递闭包，不含自身）。控制台每个键有「引用关系」页签展示
引用去向、被谁依赖与影响范围预览。

### 3.8 多租户与配额

所有数据查询都带 `tenant_id` 条件；每个租户有独立配额：
`max_namespaces`、`max_items_per_group`、`version_retention`，超限写入返回
`402 {"code":"quota_exceeded"}`。系统自动创建的 `_public` 命名空间不占命名空间配额。

---

## 4. HTTP 接口速览

除 `/healthz`、`/api/tenants` 外，请求都需要请求头 `X-Tenant-ID: <租户id>`；
写操作可带 `X-Operator: <操作者>`。

```
POST /api/tenants                      创建租户（可带 max_namespaces 等配额）
GET  /api/tenants                      租户列表
PUT  /api/tenants/:id/quota            修改配额

GET/POST /api/namespaces               命名空间列表 / 新建
GET/POST /api/namespaces/:ns/groups    分组列表 / 新建
GET/POST /api/namespaces/:ns/groups/:g/items        配置项列表 / 新建
GET      /api/items/:id                配置项详情（含各环境当前值）
PUT      /api/items/:id/schema         绑定/修改 JSON Schema
POST     /api/items/:id/values         提交新值（可带 gray 灰度）
GET      /api/items/:id/versions?env=  版本历史
POST     /api/items/:id/rollback       回退到某版本（产生新版本）
GET      /api/items/:id/diff?env=&from=&to=   两版本行级对比

GET      /api/releases?namespace=      灰度发布列表
GET      /api/releases/:id             灰度详情（delivered_count/total_instances）
POST     /api/releases/:id/promote     确认无误，全量推送
POST     /api/releases/:id/rollback-gray 发现问题，灰度回退

GET      /api/effective?namespace=&group=&env=   计算合并生效值（可带 instance_id/ip）
GET      /api/items/:id/refs?env=  查询一个键引用了谁（依赖图正向）
GET      /api/items/:id/impact?env= 改动前影响范围预览（反向传递闭包）
GET      /api/poll?...                  长轮询订阅
GET      /api/ws?...                    WebSocket 订阅
GET      /api/stats                     各命名空间连接数与最近推送看板
GET      /api/instances?namespace=      在线实例明细
```

### curl 示例

```bash
# 灰度提交：只让 10.0.0.1 拿到新版本
curl -s -X POST http://localhost:8080/api/items/$ID/values \
  -H 'X-Tenant-ID: demo' -H 'X-Operator: alice' -H 'Content-Type: application/json' \
  -d '{"env":"prod","value":"flag=on\n","expected_version":1,
       "gray":{"strategy":"ip","ips":["10.0.0.1"]}}'

# 客户端长轮询（带上自己的版本与实例身份）
curl 'http://localhost:8080/api/poll?namespace=payment&group=gateway&env=prod&version=2&instance_id=i-1'
```

校验失败响应（422）形如：

```json
{"error":"validation failed","errors":[
  {"line":1,"column":3,"rule":"syntax","message":"invalid JSON: ..."},
  {"field":"age","rule":"number_gte","message":"age: number must be greater than or equal to 0"}
]}
```

---

## 5. 自动化测试

### 后端（无外部依赖，直接运行）

```bash
cd backend
go test ./... -count=1
```

覆盖需求点的测试：

| 需求 | 测试 |
| ---- | ---- |
| 四种格式合法/非法拒绝、错误定位 | `internal/validator/validator_test.go` |
| Schema 逐字段、逐约束点名报错（JSON 与 YAML） | 同上 |
| 三级深度合并、嵌套字段更新与隔离、逐叶来源 | `internal/merge/merge_test.go` |
| 来源层与取值严格一致 | 同上（provenance 断言） |
| 版本号单调递增、乐观锁冲突 | `internal/tests/service_test.go` |
| 回退产生新版本且不抹中间历史 | 同上 |
| 两版本行级 diff（新增/删除/修改） | `internal/version/diff_test.go` |
| 灰度 IP 只命中名单实例 | `internal/tests/gray_test.go` |
| 灰度百分比命中比例落在预期区间且确定性稳定 | `gray_test.go` + `internal/gray/gray_test.go` |
| 全量推送后所有实例拿到新值 | `gray_test.go` |
| 灰度回退只唤醒收到过灰度的实例 | `gray_test.go` |
| 多租户数据互不可见 | `service_test.go::TestTenantIsolation` |
| 跨租户引用被拒、跨环境 `?env=` 引用 | `refs_live_test.go` |
| 占位符词法：裸键/全限定/多占位/转义/普通文本不误伤/非法报错 | `internal/ref/ref_test.go` |
| 结构化掩码（JSON/YAML/TOML 引号内外、三引号） | `internal/ref/mask_test.go` |
| 环检测（A→B→C→A、自引用、深链迭代 DFS、闭环顺序） | `internal/graph/graph_test.go` |
| 反向传递闭包（含环安全、去重） | `graph_test.go` |
| 解引用为生效值、链式 A→B→C、来源标注不断链 | `refs_test.go` |
| 四种格式引用后仍合法、整段标量保类型 | `refs_test.go` |
| 提交守门：目标不存在 / 成环 / 自引用拒绝且不落库 | `refs_test.go`、`api/refs_api_test.go` |
| 读取时环检测不崩不挂、目标无值显式报错 | `refs_live_test.go`、`refs_test.go` |
| 传递闭包影响范围、正/反向图查询 | `refs_test.go`、`refs_live_test.go` |
| 改被引用键：依赖键版本推进、长轮询被唤醒 | `refs_live_test.go` |
| 灰度×引用：命中/未命中实例看到新/旧解引用值、推送按链过滤 | `refs_live_test.go` |
| 无引用老配置渲染与来源零变化 | `internal/merge/refs_compat_test.go` |
| 命名空间/配置项配额、版本保留数 | `service_test.go` |
| 长轮询：有更新立即返回 / 挂起 / 30s 超时提示重连 | `internal/api/api_test.go` |
| WebSocket：首帧快照 + 变更主动推送 | `api_test.go` |
| API 边界租户守卫 | `api_test.go` |

### 前端

```bash
cd frontend
npm install
npm run build      # tsc 类型检查 + vite 生产构建
```

---

## 6. 设计说明（关键决策）

- **合并内核纯函数化**（`merge.Merge`）：不碰数据库与网络，输入三层文本、输出合并值与
  来源表，因此可以被独立、充分地单测；service 层只负责从三层容器取数后调用它。
- **灰度的正确性在两处用同一套规则把关**：拉取路径（`service.servedValue`，决定
  返回哪一版值）与推送路径（`push.Gate`，决定唤醒哪条连接）都调用 `gray` 包，
  避免「推了但拉不到 / 拉到了却没推」的不一致。
- **灰度百分比用哈希而非 `rand()`**：保证灰度集合在发布期间稳定、可重复、可测，
  也让灰度后才连上的实例得到与既有实例一致的判定。
- **下发计数在单机用内存集合、多机用 Redis SET（SADD/SCARD）**，只对真正推送成功的
  实例去重计数。
- **公共层变更扇出**：`_public` 的一次改动会向该租户每个业务命名空间各发一条事件。
- **引用解析纯函数化、解引用发生在灰度选版之后**：占位符词法（`ref`）与图算法（`graph`）
  不碰数据库与网络，可独立充分单测；service 的 `resolver` 先按实例算出每层该看的版本
  （含灰度旧值），再在合并结果上迭代式 DFS 解引用（显式栈，遇环即停并报完整链路，
  不会无限展开也不会栈溢出）。提交期用 `latestOnly` 解析器在「代入后」做语法/Schema
  校验，使含占位符的结构化文本也能被正确验证。
- **版本时钟与引用扇出**：租户级单调 revision 让「生效版本」覆盖整条引用闭包；提交、
  全量、灰度回退都沿反向传递闭包为受牵连的键补发事件，灰度事件整链共用同一 release id，
  由 Gate 用同一套确定性实例选择把关，灰度与引用互不穿帮。
- **结构化占位符靠掩码过解析关**：JSON/YAML/TOML 解码前，`ref.Mask` 用引号感知状态机把
  引号外整段占位符换成 PUA 哨兵字符串，引号内保持原文；老配置不含 `@` 时整体跳过，
  零开销、字节不变。
