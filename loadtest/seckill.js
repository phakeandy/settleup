// 秒杀压测:单商品(product_id=1, 库存 1000),3 秒内打 10000 个下单请求。
// 每个请求必须带唯一 idempotency_key,否则测的是 1062 replay 分支而不是真实下单路径。
//
// 用法:
//   1. mysql -h127.0.0.1 -P3306 -uroot -proot settleup < loadtest/seed.sql
//   2. k6 run loadtest/seckill.js
//   3. 跑完去 MySQL 里验证是否超卖(见 README 或本文件末尾注释)

import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.2.0/index.js';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const PRODUCT_ID = Number(__ENV.PRODUCT_ID || 1);

// 三个我们关心的自定义计数器,跑完后在 summary 里能看到。
const created = new Counter('orders_created');       // 201,下单成功
const outOfStock = new Counter('orders_out_of_stock'); // 400 insufficient stock
const otherErrors = new Counter('orders_other_error'); // 非预期状态码

export const options = {
  scenarios: {
    seckill: {
      executor: 'constant-arrival-rate',
      rate: 10000,          // 10000 次迭代
      timeUnit: '3s',        // 分摊在 3 秒内 —— 这就是"秒杀"的瞬时打满
      duration: '3s',
      preAllocatedVUs: 3000, // Round 2 实测 vus 顶到 2000 上限、丢了 7626 个迭代——
      maxVUs: 6000,          // 说明 2000 不够,开放系统的语义在那一刻退化成了封闭系统。
                              // 这次给够余量,让到达率真正不受 VU 池限制。
    },
  },
  thresholds: {
    // 先不设硬性阈值卡断言,压测的目的是测出真实数字,不是让脚本"通过"。
  },
};

export default function () {
  const payload = JSON.stringify({
    product_id: PRODUCT_ID,
    quantity: 1,
    idempotency_key: uuidv4(),
  });

  const res = http.post(`${BASE_URL}/api/orders`, payload, {
    headers: { 'Content-Type': 'application/json' },
  });

  check(res, {
    'status is 201 or 400': (r) => r.status === 201 || r.status === 400,
  });

  if (res.status === 201) {
    created.add(1);
  } else if (res.status === 400) {
    outOfStock.add(1);
  } else {
    otherErrors.add(1);
    console.error(`unexpected status ${res.status}: ${res.body}`);
  }
}

// 压测结束后,在 MySQL 里验证是否超卖:
//
//   SELECT COUNT(*) FROM orders WHERE status IN (1,2);   -- 应该 <= 1000 (= seed 的 total)
//   SELECT available FROM inventories WHERE product_id = 1; -- 应该 >= 0 且 = 1000 - 上面那个 COUNT
//
// 三个数对照 k6 输出看:
//   - QPS: summary 里的 http_reqs 除以实际耗时(constant-arrival-rate 下约等于配置的 rate)
//   - p99: http_req_duration{p(99)}
//   - 是否超卖: orders_created 计数器的值,应该恰好等于上面 SQL 查到的订单数,且 <= 1000
