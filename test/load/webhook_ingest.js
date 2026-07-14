// webhook_ingest.js —— 告警接入端点（POST /api/v1/webhook/:token）最小压测脚本。
//
// 验证 ADR-0002 / docs/architecture.md「非功能基线」中的接入吞吐目标
// （≥ 1000 events/min），测试方法与实测基线记录在 docs/operations.md「容量规划」。
//
// 用法（需先 make dev-up + migrate + seed-demo，并启动后端）：
//
//   TOKEN=$(vigil 接入点 token) k6 run test/load/webhook_ingest.js
//
// 可调参数（环境变量）：
//   BASE_URL      目标基地址（默认 http://localhost:8080）
//   TOKEN         接入点 token（必填，seed-demo 输出的 vig_int_ 开头串）
//   RATE_PER_MIN  目标到达速率（默认 1000，events/min）
//   DURATION      持续时长（默认 3m）
//   SERVICE       告警 labels.service（默认 demo-orders，命中 seed-demo 演示服务）
//   RUN_ID        本轮标识（默认取启动时间戳；事件 id 含 RUN_ID 保证跨轮唯一，
//                 避开 5min 去重窗口——重复 dedup_key 会被分诊直接丢弃，测不到真实链路）
//
// ⚠️ 单接入点默认限流 600/min（VIGIL_INGESTION_RATE_LIMIT_PER_MIN）低于 1000/min 目标，
//    压测前须对被测后端提高该值（或按 Integration.config.rate_limit 覆盖），
//    否则超出部分收到 429（属限流保护正常行为，非吞吐瓶颈）。
//
// 判定标准（thresholds）：
//   · 非 2xx 比例 < 1%（限流/背压/落库失败都会体现在这里）
//   · 接入 ack 延迟 P95 < 1s（端点契约是「秒级返回 202」）
//   · dropped_iterations ≈ 0（k6 按到达率调度，掉迭代说明被测端或压测机跟不上目标速率）

import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.TOKEN;
const RATE_PER_MIN = parseInt(__ENV.RATE_PER_MIN || '1000', 10);
const DURATION = __ENV.DURATION || '3m';
const SERVICE = __ENV.SERVICE || 'demo-orders';
const RUN_ID = __ENV.RUN_ID || String(Date.now());

if (!TOKEN) {
  throw new Error('缺少 TOKEN 环境变量（seed-demo 输出的接入点 token，vig_int_ 开头）');
}

export const options = {
  scenarios: {
    ingest: {
      // 到达率驱动（开环）：按固定速率发起请求，不受响应快慢影响——
      // 贴近真实告警源行为，能暴露"被测端变慢导致排队"的问题。
      executor: 'constant-arrival-rate',
      rate: RATE_PER_MIN,
      timeUnit: '1m',
      duration: DURATION,
      preAllocatedVUs: 20,
      maxVUs: 200,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<1000'],
    checks: ['rate>0.99'],
    dropped_iterations: ['count<10'],
  },
};

// 三档 severity 轮换：critical/warning 走完整"建单 + 升级链"，info 只落 Event，
// 混合负载贴近真实告警分布，也覆盖分诊的不同分支。
const SEVERITIES = ['critical', 'warning', 'info'];

export default function () {
  const id = `loadtest-${RUN_ID}-${__VU}-${__ITER}`;
  const severity = SEVERITIES[__ITER % SEVERITIES.length];
  const payload = JSON.stringify({
    id: id,
    severity: severity,
    status: 'firing',
    summary: `[压测] webhook 接入吞吐验证 ${id}`,
    labels: { service: SERVICE, loadtest: RUN_ID },
  });

  const res = http.post(`${BASE_URL}/api/v1/webhook/${TOKEN}`, payload, {
    headers: { 'Content-Type': 'application/json' },
  });

  check(res, {
    'ack 202': (r) => r.status === 202,
  });
}
