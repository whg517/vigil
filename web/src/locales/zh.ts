/**
 * 中文文案（权威源 / authoritative source）。
 *
 * 组织约定：按「区域/页面」分节命名空间（nav / common / enum / login / ...）。
 * - zh 是权威：新增文案先在此定义 key，再到 en.ts 补对应翻译。
 * - key 结构必须与 en.ts 完全一致（缺 key 会 fallback 到 zh，导致英文界面残留中文）。
 * - 详见 web/README.md「国际化（i18n）」一节。
 */
const zh = {
  common: {
    confirm: "确认",
    cancel: "取消",
    save: "保存",
    delete: "删除",
    edit: "编辑",
    create: "新建",
    close: "关闭",
    loading: "加载中…",
    submitting: "提交中…",
    all: "全部",
    prev: "上一页",
    next: "下一页",
    pageInfo: "第 {{page}} / {{total}} 页",
  },
  nav: {
    dashboard: "仪表盘",
    incidents: "事件",
    oncall: "值班排班",
    services: "服务",
    maintenance: "维护窗口",
    integrations: "接入管理",
    webhookSubscriptions: "出站订阅",
    ticketIntegrations: "工单集成",
    credentials: "凭据托管",
    escalationPolicies: "升级策略",
    usersTeams: "用户与团队",
    runbooks: "Runbook",
    postmortems: "复盘",
    settings: "设置",
    logout: "登出",
    language: "语言",
  },
  enum: {
    severity: {
      critical: "严重",
      warning: "警告",
      info: "信息",
    },
    status: {
      triggered: "待响应",
      escalated: "已升级",
      acked: "已确认",
      resolved: "已解决",
      closed: "已关闭",
    },
  },
  login: {
    title: "Vigil 登录",
    subtitle: "告警处置平台 · 守夜人",
    username: "用户名",
    password: "密码",
    submit: "登录",
    submitting: "登录中...",
    hint: "默认管理员 admin / changeme，首次登录后请立即改密",
  },
  changePassword: {
    title: "修改密码",
    subtitle: "为保障账号安全，请设置新密码",
    current: "当前密码",
    new: "新密码",
    confirm: "确认新密码",
    newPlaceholder: "至少 8 位，含字母与数字/符号",
    confirmPlaceholder: "再次输入新密码",
    submit: "修改密码",
    submitting: "提交中...",
    hint: "修改成功后将退出登录，请用新密码重新登录",
    errMismatch: "两次输入的新密码不一致",
    errSameAsOld: "新密码不能与旧密码相同",
    errTooShort: "新密码至少 8 位",
    successToast: "密码已修改，请用新密码重新登录",
  },
  dashboard: {
    title: "仪表盘",
    overview: "近 7 天概览 · 实时刷新",
    wall: "值班大屏",
    viewIncidents: "查看事件",
    kpiActive: "活跃事件",
    kpiAlerts7d: "近 7 天告警",
    kpiMtta: "MTTA 平均确认",
    kpiMttr: "MTTR 平均解决",
    noiseRate: "降噪率 {{rate}}%",
    severityDist: "事件严重度分布（近 7 天）",
    teamLoad: "团队负载（事件数）",
    noIncidentData: "暂无事件数据",
    noTeamData: "暂无团队数据",
    teamFallback: "团队 {{id}}",
  },
  incidents: {
    title: "事件",
    summary: "共 {{total}} 条 · 按状态与严重度筛选",
    filterStatus: "状态",
    filterSeverity: "严重度",
    colNumber: "编号",
    colTitle: "标题",
    colSeverity: "严重度",
    colStatus: "状态",
    colEscalation: "升级",
    colCreatedAt: "创建时间",
    escalatedTimes: "{{count}}次",
    empty: "暂无事件",
    emptyHint: "当前筛选条件下没有事件",
  },
  settings: {
    title: "设置",
    subtitle: "平台配置：IM 平台、权限、通知规则。",
    tabRbac: "权限（RBAC）",
    tabApikey: "API Key",
    tabAudit: "审计日志",
    tabNotification: "通知配置",
    tabSubscription: "我的订阅",
    tabIm: "IM 平台",
  },
};

export default zh;

/**
 * Resources —— 文案资源的结构类型（zh 推导，值为 string）。
 * en.ts 以此为类型约束，保证 key 结构与 zh 完全一致（缺/多 key 编译报错）。
 */
export type Resources = typeof zh;
