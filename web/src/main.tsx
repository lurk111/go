import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";

type Dict = Record<string, any>;
type Event = {
	check_id?: string;
	entity_key?: string;
	title?: string;
  event_id: string;
  task_id: string;
  status: string;
  rule_code: string;
  opened_at: string;
  last_seen_at: string;
  close_reason: string | null;
  diagnosis_status: string;
  episode: number;
  evidence: Dict;
};
const labels: Record<string, string> = {
  TASK_FAILED: "任务执行失败",
  QUEUE_DELAY: "任务排队超时",
  EXECUTION_STALE: "运行停滞候选",
  RESULT_MISSING: "任务结果缺失",
  RESULT_EMPTY_SUSPECT: "空结果待复核",
  RESULT_STRUCTURE_INVALID: "结果结构异常",
  SOURCE_STATE_UNKNOWN: "未知任务状态",
  MONITOR_SOURCE_UNAVAILABLE: "业务数据源不可用",
  open: "待处理",
  resolved: "已恢复",
  closed_unresolved: "人工关闭 · 未确认恢复",
  queued: "等待诊断",
  running: "正在诊断",
  completed: "诊断完成",
  failed: "诊断失败",
  timed_out: "诊断超时",
  canceled: "已取消",
  not_requested: "尚未诊断",
  pending: "等待投递",
  sending: "正在投递",
  retry: "等待重试",
  delivered: "已送达",
  needs_attention: "投递需关注",
  initial: "初始提醒",
  diagnosis: "诊断更新",
  closed: "人工关闭",
  rule_based: "规则诊断",
  model_assisted: "规则 + AI 分析",
  simulated: "历史模拟报告",
  insufficient_evidence: "证据不足",
  confirmed_by_error: "明确错误支持",
  supported_hypothesis: "有证据支持的假设",
  opened: "发现异常",
  condition_cleared: "业务条件解除",
  manually_closed: "人工关闭",
  diagnosis_completed: "诊断完成",
  diagnosis_canceled: "诊断取消",
  task_logs: "任务日志",
  worker_status: "Worker 心跳",
  extraction_summary: "提取摘要",
  model_error: "模型调用错误",
  task_snapshot: "任务快照",
};
const label = (s: string) => labels[s] || s;
const date = (s: string | null) =>
  s
    ? new Date(s).toLocaleString("zh-CN", {
        timeZone: "Asia/Shanghai",
        hour12: false,
      })
    : "尚无记录";
async function api(path: string, options: RequestInit = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers: {
      "Content-Type": "application/json",
      "X-Bid-Mon": "1",
      ...options.headers,
    },
  });
  if (res.status === 401) throw new Error("登录已失效，请重新登录");
  const value = await res.json();
  if (!res.ok) throw new Error(value.error || "请求失败，请稍后重试");
  return value;
}
function Badge({ value }: { value: string }) {
  return <span className={"badge " + value}>{label(value)}</span>;
}
function App() {
	const [configText,setConfigText]=useState(""),[configMessage,setConfigMessage]=useState(""),[previewID,setPreviewID]=useState(""),[configBusy,setConfigBusy]=useState(false);
	const [sourceID,setSourceID]=useState("main-db"),[sourceDriver,setSourceDriver]=useState("mysql"),[sourceDSN,setSourceDSN]=useState(""),[history,setHistory]=useState<Dict[]>([]);
  const [authenticated, setAuthenticated] = useState(false),
    [checking, setChecking] = useState(true),
    [token, setToken] = useState(""),
    [error, setError] = useState("");
  const [tab, setTab] = useState("events"),
    [events, setEvents] = useState<Event[]>([]),
    [metrics, setMetrics] = useState<Dict>({}),
    [filter, setFilter] = useState("open"),
    [query, setQuery] = useState(""),
    [rule, setRule] = useState(""),
    [selected, setSelected] = useState(""),
    [detail, setDetail] = useState<Dict | null>(null),
    [notifications, setNotifications] = useState<Dict[]>([]),
    [outbox, setOutbox] = useState<Dict[]>([]),
    [reason, setReason] = useState(""),
    [busy, setBusy] = useState(false),
    [updated, setUpdated] = useState(""),
    [refresh, setRefresh] = useState(0);
  useEffect(() => {
    api("/auth/session")
      .then(() => setAuthenticated(true))
      .catch(() => {})
      .finally(() => setChecking(false));
  }, []);
  useEffect(() => {
    if (!authenticated) return;
    const tick = setInterval(() => setRefresh((v) => v + 1), 10000);
    return () => clearInterval(tick);
  }, [authenticated]);
  useEffect(() => {
    if (!authenticated) return;
    let valid = true;
    const run = async () => {
      try {
        const params = new URLSearchParams({
          limit: "500",
          status: filter,
          task_id: query,
          rule_code: rule,
        });
        const [rows, m] = await Promise.all([
          api("/internal/events?" + params),
          api("/internal/metrics"),
        ]);
        if (valid) {
          setEvents(rows);
          setMetrics(m);
          setUpdated(new Date().toISOString());
          setError("");
        }
      } catch (e) {
        if (valid) setError((e as Error).message);
      }
    };
    run();
    return () => {
      valid = false;
    };
  }, [authenticated, filter, query, rule, refresh]);
  useEffect(() => {
    if (!authenticated || !selected) {
      setDetail(null);
      return;
    }
    let valid = true;
    Promise.all([
      api(`/internal/events/${selected}/report`),
      api(`/internal/events/${selected}/audit`),
      api(`/internal/outbox?event_id=${selected}`),
    ])
      .then(([report, audit, delivery]) => {
        if (valid) setDetail({ id: selected, report, audit, delivery });
      })
      .catch((e) => {
        if (valid) setError(e.message);
      });
    return () => {
      valid = false;
    };
  }, [authenticated, selected, refresh]);
  useEffect(() => {
    if (!authenticated || tab === "events") return;
    let valid = true;
    Promise.all([api("/internal/notifications"), api("/internal/outbox")])
      .then(([n, o]) => {
        if (valid) {
          setNotifications(n);
          setOutbox(o);
        }
      })
      .catch((e) => {
        if (valid) setError(e.message);
      });
    return () => {
      valid = false;
    };
  }, [authenticated, tab, refresh]);
	useEffect(()=>{
		if(!authenticated||tab!=="config")return;
		api("/internal/config").then(c=>{if(c.enabled)setConfigText(JSON.stringify(c.document,null,2));else setConfigMessage("此运行入口为首期模式，请使用二期启动入口。")}).catch(e=>setError(e.message));
		api("/internal/config/history").then(setHistory).catch(()=>{});
	},[authenticated,tab]);
	async function editConfig(action:"validate"|"save"|"preview"){
		setConfigBusy(true);setConfigMessage("");
		try{
			const path=action==="save"?"/internal/config":action==="preview"?`/internal/checks/${encodeURIComponent(previewID)}/preview`:"/internal/config/validate";
			const result=await api(path,{method:action==="save"?"PUT":"POST",body:configText});
			if(action==="validate"){setConfigText(JSON.stringify(result.document,null,2));setConfigMessage("配置校验通过，尚未启用。");}
			else if(action==="save"){setConfigMessage("配置已启用："+result.version.slice(0,12));setRefresh(v=>v+1);setHistory(await api("/internal/config/history"));}
			else setConfigMessage("试运行结果（不建事件、不通知）：\n"+JSON.stringify(result,null,2));
		}catch(e){setConfigMessage((e as Error).message)}finally{setConfigBusy(false)}
	}
	async function addSource(e:React.FormEvent){
		e.preventDefault();setConfigBusy(true);
		try{
			const doc=JSON.parse(configText);const key="source-"+sourceID;
			await api("/internal/secrets/"+encodeURIComponent(key),{method:"PUT",body:JSON.stringify({value:sourceDSN})});
			doc.sources=[...(doc.sources||[]).filter((s:Dict)=>s.id!==sourceID),{id:sourceID,driver:sourceDriver,dsn:{file:"stored:"+key},budget:{timeout:"5s",max_rows:1000,max_bytes:1048576}}];
			setConfigText(JSON.stringify(doc,null,2));setSourceDSN("");setConfigMessage("连接凭据已加密保存。配置编辑区已加入数据源，设置检查后校验并启用。");
		}catch(e){setConfigMessage((e as Error).message)}finally{setConfigBusy(false)}
	}
	function addCheckTemplate(){
		try{
			const doc=JSON.parse(configText);const source=doc.sources?.[0];if(!source)throw Error("请先添加数据源。");
			const id="check-"+Date.now();doc.checks=[...(doc.checks||[]),{id,source:source.id,mode:"entity",environment:"default",interval:"15s",query:"SELECT id, status, updated_at FROM jobs",identity:["id"],fields:{id:{column:"id",type:"string",required:true},status:{column:"status",type:"string",required:true},updated_at:{column:"updated_at",type:"timestamp",required:true}},rules:[{id:"failed",version:"v1",trigger:"fields.status == 'failed'",recover:"fields.status == 'completed'",confirmations:2,recovery_confirmations:2,severity:"warning",summary:"业务执行失败"}],diagnosis_mode:"rule_only",allow_model:false,notification_routes:[]}];
			setConfigText(JSON.stringify(doc,null,2));setPreviewID(id);setConfigMessage("已加入检查模板。请按你的表结构修改 query、fields 和规则，再试运行。");
		}catch(e){setConfigMessage((e as Error).message)}
	}
	async function testConfigPart(kind:"sources"|"channels",id:string){
		if(!id)return;setConfigBusy(true);try{const result=await api(`/internal/${kind}/${encodeURIComponent(id)}/test`,{method:"POST",body:configText});setConfigMessage(JSON.stringify(result,null,2))}catch(e){setConfigMessage((e as Error).message)}finally{setConfigBusy(false)}
	}
	function toggleCheck(id:string){try{const doc=JSON.parse(configText);doc.checks=doc.checks.map((c:Dict)=>c.id===id?{...c,enabled:c.enabled===false}:c);setConfigText(JSON.stringify(doc,null,2));setConfigMessage("启停选择已写入编辑区，保存后生效。暂停不会自动关闭异常。")}catch(e){setConfigMessage("请先校验并转换为 JSON 配置。")}}
	let editableChecks:Dict[]=[],editableChannels:Dict[]=[];try{const c=JSON.parse(configText);editableChecks=c.checks||[];editableChannels=c.channels||[]}catch{}
  async function login(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/auth/login", {
        method: "POST",
        body: JSON.stringify({ token }),
      });
      setToken("");
      setAuthenticated(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function closeEvent(e: React.FormEvent) {
    e.preventDefault();
    if (!selected || !reason.trim()) return;
    setBusy(true);
    try {
      await api(`/internal/events/${selected}/close`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      });
      setReason("");
      setFilter("closed_unresolved");
      setRefresh((v) => v + 1);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (checking) return <div className="loading">正在连接观察台…</div>;
  if (!authenticated)
    return (
      <main className="login">
        <div className="login-copy">
          <div className="eyebrow">BID_MON / LOCAL WORKSPACE</div>
          <h1>
            把任务异常，
            <br />
            变成可处理的信息。
          </h1>
          <p>自动发现 · 证据诊断 · 人工处理</p>
          <small>业务监测与诊断平台</small>
        </div>
        <form onSubmit={login} className="login-card">
          <span className="mark">b.</span>
          <h2>进入本地观察台</h2>
          <p>使用本次安装生成的管理员凭据登录。</p>
          <label htmlFor="token">管理凭据</label>
          <input
            id="token"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            required
            autoComplete="off"
            placeholder="输入管理凭据"
          />
          {error && (
            <p role="alert" className="error">
              {error}
            </p>
          )}
          <button disabled={busy}>{busy ? "正在验证…" : "登录观察台 →"}</button>
          <small>会话仅用于本机管理，有效期 8 小时。</small>
        </form>
      </main>
    );
  const event = events.find((e) => e.event_id === selected),
    d = detail?.id === selected ? detail : null,
    report = d?.report?.report;
  const scanAge = metrics.last_scan_at
    ? (Date.now() - new Date(metrics.last_scan_at).getTime()) / 1000
    : Infinity;
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="mark">b.</span>
          <div>
            bid_mon<small>{metrics.config_version?"业务监测与诊断":"售前任务观察台"}</small>
          </div>
        </div>
        <nav>
          {[
            ["events", "01", "异常事件"],
            ["notifications", "02", "通知记录"],
            ["health", "03", "运行状态"],
			["config", "04", "监测配置"],
          ].map(([id, n, title]) => (
            <button
              key={id}
              className={tab === id ? "active" : ""}
              onClick={() => setTab(id)}
            >
              <span>{n}</span>
              {title}
            </button>
          ))}
        </nav>
        <div className="sidebar-foot">
          <span className="dot" /> {metrics.config_version?"独立监测实例":"本机实验环境"}
          <p>
            业务观察确认恢复
            <br />
            人工关闭保留原因
          </p>
          <button
            onClick={async () => {
              try {
                await api("/auth/logout", { method: "POST" });
                setAuthenticated(false);
                setSelected("");
                setError("");
              } catch (e) {
                setError((e as Error).message);
              }
            }}
          >
            退出登录 ↗
          </button>
        </div>
      </aside>
      <main className="workspace">
        <header>
          <div>
            <div className="eyebrow">BUSINESS MONITORING / DIAGNOSIS</div>
            <h1>
              {tab === "events"
                ? "异常事件"
                : tab === "config" ? "监测配置" : tab === "notifications"
                  ? "通知记录"
                  : "运行状态"}
            </h1>
            <p>
              {tab === "events"
                ? "发现异常，查看依据，再决定如何处理。"
                : tab === "notifications"
                  ? "查看通知确认记录与发送结果。"
				: tab === "config" ? "连接业务数据，设置规则，先试运行再启用。"
                  : "关注采集、诊断和通知是否正常运行。"}
            </p>
          </div>
          <div className="header-side">
            <span className="environment">LOCAL · 独立 Go 服务</span>
            <button
              className="secondary"
              onClick={() => setRefresh((v) => v + 1)}
            >
              刷新 ↻
            </button>
            <small>更新于 {updated ? date(updated) : "—"}</small>
          </div>
        </header>
        {error && (
          <div className="error" role="alert">
            {error}
            {error.includes("登录") && (
              <button onClick={() => setAuthenticated(false)}>重新登录</button>
            )}
          </div>
        )}
        <section className="stats">
          {[
            ["待处理异常", metrics.events?.open || 0, "等待负责人复核"],
            ["业务已恢复", metrics.events?.resolved || 0, "依据业务观察确认"],
            [
              "人工已关闭",
              metrics.events?.closed_unresolved || 0,
              "关闭不代表恢复",
            ],
            [
              "投递需关注",
              metrics.outbox?.needs_attention || 0,
              "已达到重试上限",
            ],
          ].map(([name, count, note]) => (
            <div className="stat" key={name as string}>
              <span>{name}</span>
              <strong>{count}</strong>
              <small>{note}</small>
            </div>
          ))}
        </section>
        {tab === "events" && (
          <>
            <div className="filters">
              <label>
                处理状态
                <select
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                >
                  <option value="">全部状态</option>
                  <option value="open">待处理</option>
                  <option value="resolved">已恢复</option>
                  <option value="closed_unresolved">人工关闭</option>
                </select>
              </label>
              <label>
                异常类型
                <select value={rule} onChange={(e) => setRule(e.target.value)}>
                  <option value="">全部类型</option>
                  {Array.from(new Set([...Object.keys(labels).filter((k)=>k===k.toUpperCase()),...events.map((e)=>e.rule_code)]))
                    .map((k) => (
                      <option key={k} value={k}>
                        {label(k)}
                      </option>
                    ))}
                </select>
              </label>
              <label className="search">
                业务对象 ID
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="输入任务 ID 筛选"
                />
              </label>
            </div>
            <div className="split">
              <section className="panel event-list">
                <div className="panel-title">
                  <h2>事件列表</h2>
                  <small>显示 {events.length} 条 · 最多 500 条</small>
                </div>
                {events.length === 0 ? (
                  <div className="empty">
                    当前筛选条件下没有异常事件。
                    <p>系统会自动发现新任务，无需手工登记。</p>
                  </div>
                ) : (
                  events.map((e) => (
                    <button
                      className={
                        "event-row " +
                        (selected === e.event_id ? "selected" : "")
                      }
                      key={e.event_id}
                      onClick={() => {
                        setSelected(e.event_id);
                        setReason("");
                      }}
                    >
                      <div className="row-top">
                        <strong>{e.title||label(e.rule_code)}</strong>
                        <Badge value={e.status} />
                      </div>
                      <code>{e.entity_key||e.task_id}</code>
					{e.check_id&&<small>检查：{e.check_id}</small>}
                      <div className="row-bottom">
                        <span>{date(e.opened_at)}</span>
                        <span>第 {e.episode} 次事件 →</span>
                      </div>
                    </button>
                  ))
                )}
              </section>
              <section className="panel detail">
                {!event ? (
                  <div className="empty detail-empty">
                    <span className="detail-symbol">◎</span>
                    <h2>从事件开始，顺着证据判断</h2>
                    <p>选择左侧事件查看诊断、证据和处理记录。</p>
                  </div>
                ) : (
                  <>
                    <div className="panel-title">
                      <h2>{event.title||label(event.rule_code)}</h2>
                      <Badge value={event.status} />
                    </div>
                    <div className="detail-body">
                      <div className="identity">
                        <span>业务对象 ID</span>
                        <code>{event.entity_key||event.task_id}</code>
                        <small>
                          发现于 {date(event.opened_at)} · 最近观察{" "}
                          {date(event.last_seen_at)}
                        </small>
                      </div>
                      {event.close_reason && (
                        <div className="notice">
                          <strong>人工关闭原因</strong>
                          <p>{event.close_reason}</p>
                          <small>此操作不表示业务恢复。</small>
                        </div>
                      )}
                      <h3>诊断结论</h3>
                      {!d ? (
                        <p>正在读取诊断记录…</p>
                      ) : report && Object.keys(report).length > 0 ? (
                        <div className="report">
                          <div>
                            <Badge value={report.diagnosis_mode} />
                            <Badge value={report.certainty} />
                          </div>
                          <h3>{report.summary}</h3>
                          {report.model_status === "fallback" && <p className="notice">AI 分析未完成，已保留规则诊断。原因：{report.model_error}</p>}
                          {report.model_analysis && <div><strong>AI 证据分析 · {report.model_name}</strong><p>{report.model_analysis.text}</p><small>依据：{report.model_analysis.evidence_ids?.join("，")}</small>{report.model_analysis.reference_labels && <details><summary>分析中的证据编号对照</summary>{Object.entries(report.model_analysis.reference_labels).map(([short, id]) => <small key={short}>{short} → {String(id)}</small>)}</details>}</div>}
                          {report.causes?.map((f: Dict, i: number) => (
                            <p key={i}>
                              {f.text}
                              <small>依据：{f.evidence_ids?.join("，")}</small>
                            </p>
                          ))}
                          {report.recommended_actions?.map(
                            (a: string, i: number) => (
                              <p key={i}>建议 · {a}</p>
                            ),
                          )}
                          {report.model_recommended_actions?.map((a: string, i: number) => <p key={"ai-" + i}>AI 建议（人工核对） · {a}</p>)}
                          {report.missing_evidence?.length > 0 && (
                            <details>
                              <summary>
                                缺失的证据（{report.missing_evidence.length}）
                              </summary>
                              <ul>
                                {report.missing_evidence.map(
                                  (m: string, i: number) => (
                                    <li key={i}>{m}</li>
                                  ),
                                )}
                              </ul>
                            </details>
                          )}
                        </div>
                      ) : (
                        <div className="notice">
                          <Badge value={d.report.diagnosis_status} />
                          <p>初始异常已经记录。诊断不可用时仍可人工处理。</p>
                        </div>
                      )}
                      <h3>取证记录</h3>
                      {d?.report?.evidence?.map((e: Dict) => (
                        <details className="evidence" key={e.evidence_id}>
                          <summary>
                            {label(e.operation)}
                            <span>
                              {e.unavailable_reason
                                ? "不可用"
                                : e.truncated
                                  ? "结果有截断"
                                  : "已采集"}
                            </span>
                          </summary>
                          <small>
                            {e.evidence_id} · {date(e.collected_at)}
                          </small>
                          {e.unavailable_reason && (
                            <p>不可用原因：{e.unavailable_reason}</p>
                          )}
                          <pre>{JSON.stringify(e.content, null, 2)}</pre>
                        </details>
                      ))}
                      {d && !d.report?.evidence?.length && (
                        <p className="muted">尚无取证记录。</p>
                      )}
                      <h3>通知投递</h3>
                      {d?.delivery?.map((o: Dict) => (
                        <div className="delivery" key={o.id}>
                          <span>{label(o.kind)}</span>
                          <Badge value={o.status} />
                          <small>尝试 {o.attempts} 次</small>
                        </div>
                      ))}
                      <h3>处理时间线</h3>
                      <ol className="timeline">
                        {d?.audit?.map((a: Dict) => (
                          <li key={a.id}>
                            <span>{label(a.action)}</span>
                            <small>{date(a.at)}</small>
                            {a.details?.reason && <p>{a.details.reason}</p>}
                          </li>
                        ))}
                      </ol>
                      {event.status === "open" && (
                        <form className="close-form" onSubmit={closeEvent}>
                          <h3>人工关闭事件</h3>
                          <p>
                            关闭后停止本事件的后续提醒。系统继续观察业务条件，关闭不代表恢复。
                          </p>
                          <label htmlFor="reason">关闭原因</label>
                          <textarea
                            id="reason"
                            maxLength={1000}
                            value={reason}
                            onChange={(e) => setReason(e.target.value)}
                            placeholder="例如：这是实验故意制造的失败，不需要继续处理"
                            required
                          />
                          <button disabled={busy || !reason.trim()}>
                            {busy ? "正在保存…" : "记录原因并关闭事件"}
                          </button>
                        </form>
                      )}
                    </div>
                  </>
                )}
              </section>
            </div>
          </>
        )}
        {tab === "notifications" && (
          <section className="panel">
            <div className="panel-title">
              <h2>{metrics.config_version?"渠道确认记录":"本地接收记录"}</h2>
              <small>{metrics.config_version?"按配置渠道的确认协议记录":"不代表飞书送达"} · 最近 {notifications.length} 条</small>
            </div>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>内容</th>
                    <th>任务</th>
                    <th>类型</th>
                    <th>观察时间</th>
                  </tr>
                </thead>
                <tbody>
                  {notifications.map((n, i) => (
                    <tr key={n.event_id + "-" + n.revision + "-" + i}>
                      <td>
                        {n.summary}
                        <small>{label(n.diagnosis_mode)}</small>
                      </td>
                      <td>
                        <code>{n.task_id}</code>
                      </td>
                      <td>{label(n.kind)}</td>
                      <td>{date(n.observed_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {notifications.length === 0 && (
                <div className="empty">尚无本地通知记录。</div>
              )}
            </div>
          </section>
        )}
        {tab === "health" && (
          <>
            <div className="health-grid">
              <section className="panel health-card">
                <h2>业务数据采集</h2>
				{metrics.checks&&<div>{metrics.checks.length===0?<p>尚无检查，前往监测配置添加业务。</p>:metrics.checks.map((c:Dict)=><div className="delivery" key={c.id}><strong>{c.id}</strong><span>{!c.enabled?"已暂停":c.error?"检查失败":c.last_success_at?"正常":"等待首次检查"}</span><small>{date(c.last_success_at)} · {c.duration_ms}ms · {c.rows}条</small>{c.error&&<p>{c.error}</p>}</div>)}</div>}
                {!metrics.checks&&<><span
                  className={
                    "health-status " +
                    (scanAge < Math.max(30, metrics.poll_seconds * 3)
                      ? "ok"
                      : "bad")
                  }
                >
                  {scanAge < Math.max(30, metrics.poll_seconds * 3)
                    ? "最近扫描正常"
                    : "扫描需关注"}
                </span>
                <p>最后成功：{date(metrics.last_scan_at)}</p>
                <p>连续失败：{metrics.source_failures || 0} 次</p>
                <p>扫描周期：{metrics.poll_seconds || 15} 秒</p></>}
              </section>
              <section className="panel health-card">
                <h2>诊断任务</h2>
                <p>当前方式：{metrics.model_enabled ? `规则 + AI 分析（${metrics.model_name}）` : "规则诊断"}。</p>
                {Object.entries(metrics.diagnosis || {}).map(([k, v]) => (
                  <div className="delivery" key={k}>
                    <Badge value={k} />
                    <strong>{String(v)}</strong>
                  </div>
                ))}
              </section>
            </div>
            <section className="panel">
              <div className="panel-title">
                <h2>投递状态</h2>
                <small>最近 500 条记录</small>
              </div>
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>事件 ID</th>
                      <th>状态</th>
                      <th>尝试次数</th>
                      <th>错误</th>
                    </tr>
                  </thead>
                  <tbody>
                    {outbox.map((o) => (
                      <tr key={o.id}>
                        <td>
                          <code>{o.event_id}</code>
                        </td>
                        <td>
                          <Badge value={o.status} />
                        </td>
                        <td>{o.attempts}</td>
                        <td>{o.error || "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          </>
        )}
		{tab==="config"&&<section className="panel config-panel">
			<div className="panel-title"><h2>业务接入与监测定义</h2><small>配置保存为版本，可回退；试运行不产生告警</small></div>
			<form className="config-source" onSubmit={addSource}>
				<h3>添加数据库连接</h3><p>使用只读账号。连接凭据加密保存，配置只保留引用。Docker Desktop 访问宿主使用 host.docker.internal，远端使用真实主机地址。</p>
				<label>来源 ID<input value={sourceID} onChange={e=>setSourceID(e.target.value)} pattern="[A-Za-z][A-Za-z0-9_-]{0,55}" required/></label>
				<label>数据库<select value={sourceDriver} onChange={e=>setSourceDriver(e.target.value)}><option value="mysql">MySQL</option><option value="postgres">PostgreSQL</option></select></label>
				<label>只读连接字符串<input type="password" autoComplete="off" value={sourceDSN} onChange={e=>setSourceDSN(e.target.value)} placeholder={sourceDriver==="mysql"?"reader:password@tcp(host:3306)/database?parseTime=true&loc=UTC":"postgres://reader:password@host:5432/database?sslmode=require"} required/></label>
				<button disabled={configBusy||!configText}>保存凭据并添加来源</button>
				<button className="secondary" type="button" disabled={configBusy||!configText} onClick={()=>testConfigPart("sources",sourceID)}>测试此来源连接</button>
			</form>
			<div className="config-actions"><button className="secondary" onClick={addCheckTemplate} disabled={!configText}>添加业务检查模板</button><button className="secondary" onClick={()=>{const b=new Blob([configText],{type:"application/json"});const u=URL.createObjectURL(b);const a=document.createElement("a");a.href=u;a.download="monitor-config.json";a.click();URL.revokeObjectURL(u)}} disabled={!configText}>导出配置</button></div>
			<label>配置（JSON / YAML）<textarea className="config-editor" aria-label="监测配置编辑器" value={configText} onChange={e=>setConfigText(e.target.value)} spellCheck={false}/></label>
			<div className="config-actions"><button className="secondary" disabled={configBusy||!configText} onClick={()=>editConfig("validate")}>校验配置</button><label>试运行检查 ID<input value={previewID} onChange={e=>setPreviewID(e.target.value)}/></label><button className="secondary" disabled={configBusy||!previewID} onClick={()=>editConfig("preview")}>只读试运行</button><button disabled={configBusy||!configText} onClick={()=>editConfig("save")}>保存并启用配置</button></div>
			{configMessage&&<pre className="config-result" role="status">{configMessage}</pre>}
			{editableChecks.map(c=><div className="delivery" key={c.id}><strong>{c.id}</strong><span>{c.enabled===false?"已暂停":"启用"}</span><button className="secondary" disabled={configBusy} onClick={()=>toggleCheck(c.id)}>{c.enabled===false?"启用检查":"暂停检查"}</button></div>)}
			{editableChannels.map(c=><div className="delivery" key={c.id}><strong>通知：{c.id}</strong><button className="secondary" disabled={configBusy} onClick={()=>testConfigPart("channels",c.id)}>发送合成测试消息</button></div>)}
			<div className="config-actions"><button className="secondary" disabled={configBusy} onClick={async()=>{setConfigBusy(true);try{const result=await api("/internal/retention/preview",{method:"POST",body:JSON.stringify({closed_evidence_days:30,snapshot_days:30,delivered_days:30})});setConfigMessage("30 天保留策略预览，不删除数据：\n"+JSON.stringify(result,null,2))}catch(e){setConfigMessage((e as Error).message)}finally{setConfigBusy(false)}}}>查看 30 天保留预览</button><small>自动清理已关闭</small></div>
			{history.length>0&&<details><summary>配置历史与回退</summary>{history.map(h=><div className="delivery" key={h.version}><code>{h.version.slice(0,12)}</code><small>{date(h.created_at)}</small><button className="secondary" disabled={h.active||configBusy} onClick={async()=>{setConfigBusy(true);try{await api("/internal/config/rollback/"+h.version,{method:"POST"});const c=await api("/internal/config");setConfigText(JSON.stringify(c.document,null,2));setHistory(await api("/internal/config/history"));setConfigMessage("已回退配置版本。")}catch(e){setConfigMessage((e as Error).message)}finally{setConfigBusy(false)}}}>{h.active?"当前版本":"回退到此版本"}</button></div>)}</details>}
		</section>}
        <footer>
          bid_mon · 业务监测与诊断{" "}
          <span>时间显示为北京时间 · 每 10 秒更新</span>
        </footer>
      </main>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
