# 压测日志

秒杀路径每一轮压测的记录:预期是什么、看到了什么、根因是什么、改了什么。日志原样贴出,
不转述,方便回头核对。

每一轮的结构:假设、现象、根因、修复、下一步。

## Round 1, 2026-08-23:测出连接池打满,不是行锁竞争

假设:单商品(product_id=1, total=1000)在 3 秒内打 10000 个请求,应该能看到 MySQL
在这一行库存上的行锁把请求串行化——吞吐撞顶、p99 飙升,但库存不会扣成负数。

k6 输出:

    checks_total.......: 10001  2525.387659/s
    checks_succeeded...: 4.97%  498 out of 10001
    checks_failed......: 95.02% 9503 out of 10001

    CUSTOM
    orders_created.................: 498    125.75173/s
    orders_other_error.............: 9503   2399.635929/s

    HTTP
    http_req_duration..............: avg=53.95ms min=182.22µs med=346.28µs max=1.35s p(90)=4.63ms p(95)=52.96ms
      { expected_response:true }...: avg=1.05s   min=30.48ms  med=1.19s    max=1.35s p(90)=1.27s  p(95)=1.28s
    http_req_failed................: 95.02% 9503 out of 10001
    http_reqs......................: 10001  2525.387659/s

    vus............................: 4      min=4             max=156
    vus_max........................: 500    min=500           max=500

应用日志:

    2026/08/23 22:14:17 HTTP Error: Error 1040: Too many connections
    2026/08/23 22:14:17 HTTP Error: Error 1040: Too many connections
    ...(大量重复)

95% 的失败是 `orders_other_error`,不是预期的"库存不够"400。应用日志说明了原因:
MySQL 报 `1040: Too many connections`。[internal/db/db.go](../internal/db/db.go)
的 `Init()` 只调了 `sql.Open`,没设 `SetMaxOpenConns`,Go 的连接池没有上限。500 并发
VU 一冲,应用尝试开出的连接数远超 `mysql:8.0` 镜像的默认 `max_connections`(151),
MySQL 开始拒绝超出的部分。

这一轮的数字不能用来讲行锁的故事。p99=1.28s 里混进了大量连接被拒绝后的快速失败
(注意 182.22µs 这个极低的最小值),不是排队等锁的延迟。两种失败模式从外面看很像——
延迟都会飙升、吞吐都会停滞——但成因不同,这次测到的是连接池配置缺陷,不是本来想验证
的行锁竞争。

下一步:给连接池设上限(`SetMaxOpenConns`、`SetMaxIdleConns`、`SetConnMaxLifetime`,
上限要低于 MySQL 的 `max_connections`),再跑一轮。

## Round 2, 2026-08-23:行锁把请求串行化,吞吐撞顶,库存不超卖

修复:[internal/db/db.go](../internal/db/db.go) 加了
`SetMaxOpenConns(80)` / `SetMaxIdleConns(80)` / `SetConnMaxLifetime(5min)`。

k6 输出:

    execution: local
    scenarios: seckill: 3333.33 iterations/s for 3s (maxVUs: 500-2000, gracefulStop: 30s)

    WARN[0002] Insufficient VUs, reached 2000 active VUs and cannot initialize more

    checks_total.......: 2373    300.376573/s
    checks_succeeded...: 100.00% 2373 out of 2373
    checks_failed......: 0.00%   0 out of 2373

    CUSTOM
    orders_created.................: 1000   126.580941/s
    orders_out_of_stock............: 1373   173.795632/s

    HTTP
    http_req_duration..............: avg=5.06s min=23.96ms med=5.77s max=7.87s p(90)=7.38s p(95)=7.56s
      { expected_response:true }...: avg=3.13s min=23.96ms med=2.97s max=7.39s p(90)=5.79s p(95)=6.24s
    http_req_failed................: 57.85% 1373 out of 2373
    http_reqs......................: 2373   300.376573/s

    EXECUTION
    dropped_iterations.............: 7626   965.306258/s
    vus............................: 1451   min=1040         max=2000
    vus_max........................: 2000   min=1040         max=2000

应用日志(全是预期内的 400,不是错误):

    2026/08/23 22:49:19 HTTP Error: insufficient stock
    ...(大量重复)

MySQL 验证(压测跑完后手动查的):

    SELECT COUNT(*) FROM orders WHERE status IN (1,2);
    -> 1000

    SELECT available FROM inventories WHERE product_id = 1;
    -> 0

这轮状态码全部落回 201/400(`checks_failed` 0%),说明 Round 1 的连接池噪音已经
清掉了,这次测到的是真实信号。三个数对上了:

库存不超卖。卖出订单数(1000)与查询出的已创建/已支付订单数(1000)、以及库存扣到
0 完全吻合,10000 次并发请求打过来,一件没多卖。

吞吐撞顶。目标发送速率是 3333.33/s,实际打完的请求只有 2373 个,`dropped_iterations`
高达 7626——k6 因为请求迟迟不返回、2000 个 VU 全部占满,来不及再按目标速率发新请求,
只能丢掉后面的迭代。实际吞吐(`http_reqs` 300.38/s)只有目标速率的 9%左右。

延迟炸开。`http_req_duration` 均值 5.06s,p90/p95 都在 7.4s 以上,和 max(7.87s)
挤在一起——说明整个压测窗口里请求基本没有排上队就到了测试结束,延迟没有一个"爬升
到顶再趋稳"的过程,而是从头到尾都堆在队尾。

根因:`order.CreateHandler` 里,库存那一行的更新
(`UPDATE inventories SET available = available - ? WHERE product_id = ? AND available >= ?`)
发生在一个事务内部,事务从第一条 INSERT 开始到 COMMIT 为止一直持有这一行的排他锁。
同一时刻只有一个事务能碰这一行,其余事务在 InnoDB 的锁等待队列里排队,而且排的不是
"一条 UPDATE 的时间",是"一整个事务的时间"(INSERT order、UPDATE inventory、
INSERT payment、COMMIT 这几次网络往返全算在锁持有时间里)。并发越高,队列越长,
排在后面的请求延迟越夸张,这正是 p90/p95/max 挤在一起、吞吐撞顶的原因。

这轮结果符合 README 里"压垮"这一步要证明的东西:单库单事务在高并发秒杀场景下,
正确性保住了(不超卖),但吞吐和延迟撑不住。

订正(见 Round 3):上面"其余事务在 InnoDB 的锁等待队列里排队"这句,把排队发生的
位置说错了。Round 3 用 `Innodb_row_lock%` 计数器直接测量过后,InnoDB 记录到的等锁
时间远远不够解释这么长的延迟——真正堆积的地方是 Go 的数据库连接池,不是 InnoDB
的锁队列。结构性根因(同一时刻只有一个事务能碰这一行)没有错,错的是"排队发生在
哪一层"这个细节。

## Round 3, 2026-08-24:量出真实的等锁时长,发现排队堆积的位置猜错了

目的:上一轮"行锁把请求串行化"这个结论,靠的是从吞吐反推(1/X)和从人口反推
(W/N)两条间接证据,没有一条是直接测量。这轮做两件事:一是把 k6 的 `maxVUs`
从 2000 调到 6000,看 Round 2 的"人口封顶"是不是测试工具自己的余量给小了；二是用
MySQL 自带的 `Innodb_row_lock%` 计数器,在压测前后各截一次快照,直接量出这段时间
真实的等锁时长,而不是靠公式推。

k6 输出(`loadtest/seckill.js` 的 `maxVUs` 从 2000 调到 6000,`preAllocatedVUs`
从 500 调到 3000):

    scenarios: seckill: 3333.33 iterations/s for 3s (maxVUs: 3000-6000, gracefulStop: 30s)

    checks_total.......: 4835    541.86953/s
    checks_succeeded...: 100.00% 4835 out of 4835

    CUSTOM
    orders_created.................: 1000   112.072292/s
    orders_out_of_stock............: 3835   429.797239/s

    HTTP
    http_req_duration..............: avg=6.42s min=32.61ms med=7.13s max=8.82s p(90)=8.19s p(95)=8.37s
    http_req_failed................: 79.31% 3835 out of 4835
    http_reqs......................: 4835   541.86953/s

    EXECUTION
    dropped_iterations.............: 5164   578.741314/s
    vus............................: 699    min=699          max=4378
    vus_max........................: 4471   min=3000         max=4471

这次没有再出现 `Insufficient VUs` 警告(没有顶到 6000 的硬上限,实际涨到 4471 就
自然回落了),说明 Round 2 的 2000 上限确实是余量给小了。但 `dropped_iterations`
依然有 5164——k6 在并发需求涨得快的时候,即便没顶到硬上限,也可能因为来不及实时
拉起新 VU 而丢迭代。这次的测试环境依然不是一个完全干净的开放系统,这点如实记录,
不算已解决。

低并发基线(无竞争,15 次串行请求,同一台机器):

    平均 14.6ms(13~17ms 区间)

这是一个事务从 BEGIN 到 COMMIT、加一次 HTTP 往返,不排队时的真实耗时。

`Innodb_row_lock%` 压测前后快照差值(不是全局累计值——累计值从 MySQL 启动以来一直
在加,混着 Round 1、Round 2 的历史数据):

    Innodb_row_lock_waits:  7637 → 12469   (+4832)
    Innodb_row_lock_time:   2349967 → 3019506 ms  (+669539 ms)

    这次的平均等锁时长 = 669539 / 4832 ≈ 138.6 ms

MySQL 验证:

    SELECT COUNT(*) FROM orders WHERE status IN (1,2);   -> 1000
    SELECT available FROM inventories WHERE product_id=1; -> 0

超卖检查依然干净,三轮下来都是 0。

发现:完成的请求数(4835)和 InnoDB 记录的等锁事件数(4832)几乎一一对应,说明几乎
每个跑完的请求确实都到达过 MySQL、确实排过 InnoDB 的锁队列——不是"根本没进数据库
就被挡住"。但这个等锁时长平均只有 138.6ms,加上基线的 14.6ms 执行时间,合计还不到
0.2 秒,跟实测的总延迟(avg 6.42s,p90 8.19s)差了一到两个数量级。

这推翻了 Round 2 里"1/吞吐量 ≈ 单次持锁时长"那个估算(当时算出 3.3ms,这次同样的
公式会算出 1/541.87 ≈ 1.85ms,更加对不上)。错误出在套错了模型:`1/X = 服务时间`
这个公式只在真正的单一服务台排队下成立,而这里 MySQL 连接池有 80 个连接,80 个事务
理论上可以同时"在 MySQL 里"排队等同一把锁,InnoDB 的等锁计数器只统计了它们排在
InnoDB 锁队列里的那一段——排在连接池外面、还没轮到拿一个连接名额、根本没能
`BEGIN` 事务的那一大段等待,不会被这个计数器记录,因为 MySQL 压根还没看到这些请求。

现在最合理的解释是:多出来的几秒钟,堆积在 [internal/db/db.go](../internal/db/db.go)
里那 80 个连接的池子外面,而不是 InnoDB 内部——这一步还没有直接测量证实,能证实的
办法是往 [cmd/server/main.go](../cmd/server/main.go) 那个还是 `todo` 的 `/metrics`
里加上 `db.Stats().WaitCount` / `WaitDuration`,这两个字段是 `database/sql` 自带的,
直接统计有多少次调用在等一个池内连接、总共等了多久,能把这轮的猜测变成实测。

结构性结论没有变:同一时刻只有一个事务能碰这一行,超卖没有发生,吞吐撞顶。变化的是
排队具体堆在哪一层——不是 InnoDB 的锁队列,更可能是 Go 的连接池——这个细节值得
在下一步验证清楚,再决定 Redis 方案到底绕开的是哪一层瓶颈。
