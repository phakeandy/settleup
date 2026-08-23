# settleup

一个订单/库存系统,用来验证自研任务队列 tq。

## 目标

为面试准备的"三高"学习项目。重点不是做电商,而是用可测量的方式回答一个问题:
高并发如何逼出最终一致性,以及任务队列在什么位置才真正值得引入。

一个推论先摆在这:不假设高并发,这个项目不需要 Redis、不需要队列、不需要最终一致性。
把订单、支付、库存全放进一个 MySQL 事务,就是 100% 强一致。所有复杂度存在的唯一理由,
是"单库单事务在高并发下会垮"。所以先让它真的垮。

## 方法:基线 → 压垮 → 修复

主线纪律:每引入一项复杂度,都由一个 before/after 的测量数字来支撑,不靠假设。

1. 基线。最笨但 100% 正确的版本:创建订单时,在同一个 MySQL 事务里扣库存
   (UPDATE inventories SET available=available-? WHERE product_id=? AND available>=?)。
   强一致,无 Redis,无队列,无最终一致性。

2. 压垮。场景是秒杀:单商品有限库存,上万并发在几秒内抢同一行库存。压测,测三个数:
   吞吐(QPS)、p99 延迟、是否超卖。观察 MySQL 在那一行库存上的行锁如何把请求串行化,
   吞吐撞顶、延迟飙升。此时瓶颈是测出来的,不是编的。

3. 修复。针对测出的瓶颈逐项引入机制,每项再测一遍:
   - 库存移到 Redis,Lua 原子扣减,绕开热点行锁。代价:库存(Redis)与订单(MySQL)成为
     两个存储,dual-write 出现,MySQL 事务失败需补偿回补 Redis。最终一致性由此被逼出。
   - 慢活/异步活(通知、超时调度)交给 tq,保持请求路径在高负载下短。
   - order.timeout 用 tq 延迟任务,对比全表扫描;数据量大时扫全表成本随之增长。

## 领域模型

PaymentIntent 模型:创建订单时,在一个事务里同时建 order 和一个 pending 的 payment,
返回 payment_id。支付是确认这个已存在的 payment,不是新建。

两个状态机,取值都用 INT:

    order:   1 created   2 paid       3 cancelled
    payment: 1 pending   2 succeeded  3 cancelled

两个状态机必须在同一个事务里一起流转,否则会出现"已取消的订单仍挂着可支付的凭证"这类不一致:

    支付:   order created→paid    且  payment pending→succeeded
    超时:   order created→cancelled 且 payment pending→cancelled

其他约定:金额一律整数分(amount_cent / price_cent);主键用自增整数;不使用外键,
引用完整性放在应用层(创建订单时本就要读商品,顺带校验存在)。

## 幂等

系统假设每个写入都可能被重复触发,幂等是底座。

创建订单:客户端提供 idempotency_key,orders 上加 UNIQUE 约束。直接 INSERT,撞 1062
即视为重试,查出已存在的订单原样返回(replay)。UNIQUE 约束是"是否已创建"的唯一裁判,
先 SELECT 只是优化,不能替代对 1062 的处理。

支付:不带客户端幂等键,payment_id 即幂等句柄。用条件更新
UPDATE payments SET status=succeeded WHERE id=? AND status=pending,影响行数为 0 即已付过。

tq 是 at-least-once 投递(租约 + 恢复),同一任务可能执行两次,所有 handler 必须幂等。

## HTTP API

先做 single-user。

GET /api/products

    [
        { "id": 1, "name": "", "price_cent": 12300 }
    ]

POST /api/orders

    {
        "product_id": 1,
        "quantity": 2,
        "idempotency_key": "<uuid>"
    }
    // ->
    {
        "order_id": 1,
        "payment_id": 1,
        "amount_cent": 10000
    }

GET /api/orders/{id}

    {
        "id": 1,
        "status": 1   // 1 created / 2 paid / 3 cancelled
    }

POST /api/payments

    { "payment_id": 1 }
    // 201 成功 / 4xx 失败

GET /api/payments/{id}

    {
        "id": 1,
        "status": 1   // 1 pending / 2 succeeded / 3 cancelled
    }

GET /health
GET /metrics

## tq 缺口

settleup 作为 tq 的第一个使用者,暴露出两个必须先补的缺口:

- 没有导出的生产者 API。enqueue 是包内私有方法,外部 module 无法入队,需导出 Enqueue。
- WithIdempotencyKey 目前是空壳(TODO F5),尚未实现任务去重。

## 进度

已完成并验证(单线程 + 30 并发):创建订单(事务内原子建 order+payment、幂等 replay)、
商品列表。

下一步:给创建订单加"MySQL 事务内扣库存",做出基线版本,然后压测。
