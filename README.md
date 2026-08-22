settleup: a minimal order/stock compensation service.

## 1. 项目定位

一个 **演示最终一致性** 的订单系统，不是电商系统。

核心目标只有一句话：

> 用自研 taskqueue 把「订单落库、库存扣减、异步通知、超时补偿」串成一条可靠链路。

所以不做登录、不做商品管理、不做支付渠道、不做前端。API 就是产品。

---

## 2. 用户故事

### 故事 1：创建订单（核心）

> 作为一个 API 调用方，我想提交用户、商品、数量、幂等键，系统创建订单并扣减库存。如果网络超时我重试同一个幂等键，系统不能重复创建订单，也不能重复扣库存。

验收：
- 第一次请求：创建 `created` 订单，扣减库存。
- 相同幂等键重试：返回原订单，不重复扣库存。
- 库存不足：返回明确错误，不创建订单。

### 故事 2：查询订单

> 作为一个 API 调用方，我想按订单号查询订单状态，知道它是 `created`、`paid` 还是 `cancelled`。

验收：
- `GET /v1/orders/{order_id}` 返回订单完整信息。

### 故事 3：支付订单

> 作为一个 API 调用方，我想对 `created` 订单发起支付。重复支付同一个支付幂等键，系统不能重复扣款或重复流转状态。

验收：
- 第一次支付：订单 `created -> paid`，记录支付单。
- 相同支付幂等键重试：返回原支付单，订单状态不变。
- 订单不是 `created`：拒绝支付。
- 金额不一致：拒绝支付。

### 故事 4：超时自动取消

> 作为一个系统，我想让 30 分钟内未支付的订单自动取消，并把库存回补。如果系统在取消过程中崩溃，恢复后不能重复回补库存。

验收：
- 订单创建后，系统自动安排一个 30 分钟后的取消任务。
- 到点后订单 `created -> cancelled`，写反向流水，回补库存。
- 并发/重复触发时，库存只回补一次。

### 故事 5：可靠事件投递

> 作为一个系统，我想把 `order.created`、`order.paid`、`order.cancelled` 事件可靠地投递给订阅方。订单不能已经落库但事件丢失。

验收：
- 订单和 outbox 事件在同一 MySQL 事务里提交。
- worker 异步投递事件。
- 投递失败自动重试，超过次数进入死信。
- 重复消费不会重复发布。

### 故事 6：对账

> 作为一个运维人员，我想定期检查订单、支付、流水、outbox 是否一致，异常时能看到具体问题。

验收：
- 定期扫描历史订单。
- 发现缺少流水、金额不匹配、缺少 outbox 事件等问题时，记录到对账问题表。

---

## 3. 功能点

### 3.1 HTTP API
先做 sigle-user

#### GET /api/products

    [
        {
            "id": 1,
            "name": "",
            "price_cent": 12300 // 123.00 yuan
        }
    ]

#### POST /api/orders 创建订单

    {
        "product_id": 1,
        "idempotency_key": "<uuid>",
        "quantity": 2
    }
    // ---
    {
        "payment_id": 1,
        "order_id": 1,
        "amount": 10000 // 100.00 yuan
        // other fields
    }

#### GET /api/orders/{id}

    // ---
    {
        "id": 1,
        "status": 'created' // created, paid, cancelled
    }

#### POST /api/payments

    {
        "payment_id": 1,
    }
    // ---
    // 201 created 表示成功 
    // 4xx 表示失败

#### GET /api/payments/{id}

    {
        "id": 1,
        "status": "pending" // pending, succeeded, failed, cancelled
    }

### 3.2 创建订单流程

```
POST /v1/orders
  │
  ├─ 1. 校验 user_id / product_id / quantity / idempotency_key
  ├─ 2. 按 idempotency_key 查订单，存在则 replay
  ├─ 3. 读商品价格
  ├─ 4. Redis Lua 原子扣库存
  ├─ 5. MySQL 事务写：
  │       orders
  │       ledger_entries（debit）
  │       outbox_events（order.created）
  ├─ 6. 事务提交后，入队 order.created 事件任务
  ├─ 7. 入队 order.timeout 延迟任务，RunAt = now + 30min
  └─ 8. 返回订单
```

关键点：
- Redis 扣库存和 MySQL 事务不在同一个事务里。如果 MySQL 事务失败，**补偿回补 Redis 库存**。
- 第 5 步和第 6 步之间是经典 dual-write 问题，由 outbox 模式解决（见 3.6）。

### 3.3 支付流程

```
POST /v1/payments
  │
  ├─ 1. 校验 order_id / amount / payment_idempotency_key
  ├─ 2. 按 payment_idempotency_key 查支付单，存在则 replay
  ├─ 3. 校验订单状态 = created，金额一致
  ├─ 4. MySQL 事务写：
  │       payments
  │       更新 orders.status = paid
  │       outbox_events（order.paid）
  ├─ 5. 提交后入队 order.paid 事件任务
  └─ 6. 返回支付单
```

关键点：
- 支付状态用条件更新 `UPDATE orders SET status='paid' WHERE id=? AND status='created'`，防止并发重复支付。

### 3.4 超时取消流程

两种触发方式，**都做**：

| 方式 | 作用 |
|---|---|
| 延迟任务（主） | 创建订单后入队 `order.timeout`，`RunAt=now+30min` |
| 兜底扫描（备） | worker 定期扫描 `created` 且 `created_at < now - 30min` 的订单 |

取消逻辑：

```
worker 收到 order.timeout 任务
  │
  ├─ 条件更新：UPDATE orders SET status='cancelled' WHERE id=? AND status='created'
  │       如果影响行数为 0，说明已被支付/取消，直接 return
  │
  ├─ MySQL 事务写：
  │       ledger_entries（credit）
  │       outbox_events（order.cancelled）
  │
  ├─ Redis 回补库存
  └─ 入队 order.cancelled 事件任务
```

关键点：
- 条件更新保证并发下只取消一次。
- 延迟任务和兜底扫描可能重复触发，靠条件更新 + 幂等任务兜住。
- 如果回补库存失败，要重试，不能丢。

### 3.5 事件发布

| 事件 | 触发时机 |
|---|---|
| `order.created` | 订单创建成功 |
| `order.paid` | 支付成功 |
| `order.cancelled` | 超时取消 |

事件内容示例：

```json
{
  "event_id": "evt_...",
  "topic": "order.created",
  "order_id": "ord_...",
  "user_id": "u1",
  "product_id": "p1",
  "quantity": 2,
  "amount_cent": 39800,
  "created_at": "..."
}
```

发布方式第一版做两种 publisher：
- `log_publisher`：打印日志，保证链路能跑通；
- `webhook_publisher`：POST 到配置的 URL，失败才进入重试。

### 3.6 Outbox 模式

这是项目的核心，功能点拆开：

```
orders / ledger_entries / outbox_events 在同一个 MySQL 事务里提交
                │
                ▼
        outbox_events.status = 'pending'
                │
                ▼
   relay 扫描 pending 事件 → 入队到 taskqueue → 标记 'queued'
                │
                ▼
   worker 消费任务 → 发布事件
                │
        ┌───────┴───────┐
        ▼               ▼
    发布成功          发布失败
    标记 published     retry_count++
                       next_attempt_at = now + backoff
                       │
                       ├─ retry_count < max → 标记 retrying，入延迟任务
                       └─ retry_count >= max → 移入 outbox_dead_letters
```

关键点：
- relay 扫描使用 `SELECT ... WHERE status='pending' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 100`。
- 入队失败不标记 queued，下一轮继续扫。
- 发布成功也要幂等：worker 消费时先检查事件状态，已 `published` 就跳过。

### 3.7 库存模型

Redis 里存：

```
key: inventory:{product_id}
value: available
```

扣减用 Lua：

```lua
local available = tonumber(redis.call('GET', KEYS[1]))
local quantity = tonumber(ARGV[1])
if available >= quantity then
    redis.call('DECRBY', KEYS[1], quantity)
    return 1
else
    return 0
end
```

回补用 `INCRBY`，但**要防止重复回补**——由取消流程的条件更新保证只回补一次。

### 3.8 对账任务

定期执行，扫描 1 分钟前的历史订单，检查：

| 检查项 | 异常 |
|---|---|
| 订单有 debit 流水且金额匹配 | 缺少或金额错误 |
| cancelled 订单有 credit 流水 | 缺少 |
| 非 cancelled 订单有 credit 流水 | 异常 credit |
| 有 `order.created` outbox | 缺少 |
| paid 订单有成功 payment | 缺少 |
| paid 订单有 `order.paid` outbox | 缺少 |
| cancelled 订单有 `order.cancelled` outbox | 缺少 |

异常写入 `reconciliation_issues`，用 `issue_key` 唯一约束避免重复报告。

### 3.9 可观测性

- Prometheus metrics：
  - 下单结果：`created / replay / inventory_insufficient / error`
  - 支付结果：`paid / replay / amount_mismatch / error`
  - outbox 调度：`claimed / published / retried / failed / dead`
  - 超时取消：`scanned / cancelled / restored / skipped / failed`
  - 对账：`scanned / issues / failed`
- pprof 端口。

---

## 4. 状态机

### 订单状态

```
created ──支付──> paid
   │
   └─超时取消──> cancelled
```

- 不允许 `paid -> cancelled`
- 不允许 `cancelled -> paid`
- 所有流转都用条件更新

### 支付单状态

```
created ──支付成功──> paid
   │
   └─支付失败──> failed
```

### outbox 事件状态

```
pending ──relay 入队──> queued ──worker 发布成功──> published
   │                      │
   │                      └─发布失败──> retrying ──重试──> published
   │                                      │
   │                                      └─超过次数──> dead
   └──────────────────────────────────────────────────> dead
```

---

## 5. 数据表

```sql
CREATE TABLE products (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    price_cent  BIGINT NOT NULL
);

CREATE TABLE inventories (
    product_id  VARCHAR(64) PRIMARY KEY,
    available   BIGINT NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE orders (
    id               VARCHAR(64) PRIMARY KEY,
    user_id          VARCHAR(64) NOT NULL,
    product_id       VARCHAR(64) NOT NULL,
    quantity         BIGINT NOT NULL,
    amount_cent      BIGINT NOT NULL,
    status           VARCHAR(32) NOT NULL,          -- created / paid / cancelled
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    paid_at          TIMESTAMP NULL,
    cancelled_at     TIMESTAMP NULL,
    KEY idx_orders_status_created_at (status, created_at)
);

CREATE TABLE payments (
    id               VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL,
    user_id          VARCHAR(64) NOT NULL,
    amount_cent      BIGINT NOT NULL,
    status           VARCHAR(32) NOT NULL,
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    KEY idx_payments_order_id (order_id)
);

CREATE TABLE ledger_entries (
    id          VARCHAR(64) PRIMARY KEY,
    order_id    VARCHAR(64) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    amount_cent BIGINT NOT NULL,
    direction   VARCHAR(16) NOT NULL,               -- debit / credit
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    KEY idx_ledger_order_id (order_id)
);

CREATE TABLE outbox_events (
    id              VARCHAR(64) PRIMARY KEY,
    topic           VARCHAR(128) NOT NULL,
    event_key       VARCHAR(128) NOT NULL,
    payload         JSON NOT NULL,
    status          VARCHAR(32) NOT NULL,           -- pending/queued/retrying/published/dead
    retry_count     INT DEFAULT 0,
    next_attempt_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_error      VARCHAR(512) DEFAULT '',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_outbox_status_next_attempt (status, next_attempt_at)
);

CREATE TABLE outbox_dead_letters (
    id                 VARCHAR(64) PRIMARY KEY,
    original_event_id  VARCHAR(64) NOT NULL UNIQUE,
    topic              VARCHAR(128) NOT NULL,
    event_key          VARCHAR(128) NOT NULL,
    payload            JSON NOT NULL,
    retry_count        INT NOT NULL,
    last_error         VARCHAR(512) NOT NULL,
    created_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE reconciliation_issues (
    id          VARCHAR(64) PRIMARY KEY,
    issue_key   VARCHAR(255) NOT NULL UNIQUE,
    issue_type  VARCHAR(128) NOT NULL,
    subject_type VARCHAR(64) NOT NULL,
    subject_id  VARCHAR(128) NOT NULL,
    detail      VARCHAR(1024) NOT NULL,
    status      VARCHAR(32) NOT NULL,
    detected_at TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
```

---

## 6. 任务类型

你的 taskqueue 里会有这些任务：

| 任务 | 触发 | 调度 |
|---|---|---|
| `outbox.publish` | relay 扫描到 pending outbox 事件 | 立即 |
| `outbox.publish.retry` | 发布失败 | RunAt = now + backoff |
| `order.timeout` | 创建订单成功后 | RunAt = now + 30min |
| `reconciliation.run` | 定时 | 每 5 分钟 |

任务唯一键：
- `outbox.publish`：`event_id`
- `outbox.publish.retry`：`event_id:retry_count`
- `order.timeout`：`order_id`

---

## 7. 明确不做

- 不做用户注册/登录/JWT
- 不做商品管理界面
- 不做真实支付渠道
- 不做前端页面
- 不做多币种、优惠券、运费
- 不做复杂库存（预扣/冻结/已售分层）
- 不做 Kafka/RabbitMQ 接入（第一版用你的 taskqueue）

---

## 8. 验收场景

### 正常路径

```
POST /v1/orders       → 201，订单 created，库存减少
GET  /v1/orders/{id}  → 200，订单 created
POST /v1/payments     → 200，订单 paid
GET  /v1/orders/{id}  → 200，订单 paid
```

### 幂等

```
POST /v1/orders 相同 idempotency_key 两次
  → 第一次创建，第二次返回同一个订单，库存只扣一次
```

### 超时补偿

```
创建订单后把延迟时间调成 10 秒
  → 10 秒后 worker 取消订单
  → 库存回补
  → 订单状态 cancelled
```

### 失败恢复

```
发布 outbox 事件的 webhook 前 3 次返回 500
  → 事件进入 retrying
  → 第 4 次成功，事件 published
```

---

## 9. 面试时的一分钟介绍

> “我做了一个订单/库存补偿系统，用来验证自研 taskqueue 在最终一致性场景里的作用。核心链路是：创建订单时，订单、流水、outbox 事件在同一个 MySQL 事务里提交；relay 扫描 outbox 表入队到我的 taskqueue；worker 异步发布事件。同时创建订单后会入队一个 30 分钟的延迟任务，超时未支付就条件更新取消订单并回补库存。所有 worker 消费都是幂等的，重试超限进死信。系统还做了对账任务，定期检查订单、支付、流水和 outbox 的一致性。”

---

这份 PRD 的范围已经很克制了。你接下来可以先做哪一块？我建议先做**数据库 schema + 创建订单 + outbox relay**，因为这是主干。
