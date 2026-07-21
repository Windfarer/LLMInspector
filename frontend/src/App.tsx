import { useState, useEffect, useRef, useMemo } from 'react';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, LineChart, Line } from 'recharts';
import './modal.css';
import './nav.css';
import './thinking.css';

interface RequestStats {
  id: string;
  user_id: string;
  model: string;
  start_time: string;
  ttft_ms: number;
  e2e_ms: number;
  prompt_tokens: number;
  content_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  cached_tokens: number;
  input_content: string;
  output_content: string;
  reasoning_content: string;
  tool_calls: string;
  is_thinking: boolean;
  is_streaming: boolean;
  is_completed: boolean;
}

interface ToolCallFunc {
  name: string;
  arguments: string;
}

interface ToolCall {
  id: string;
  type: string;
  function: ToolCallFunc;
}

const parseToolCalls = (json: string): ToolCall[] => {
  if (!json) return [];
  try {
    return JSON.parse(json) as ToolCall[];
  } catch {
    return [];
  }
};

const formatToolArguments = (args: string): string => {
  if (!args) return '';
  try {
    return JSON.stringify(JSON.parse(args), null, 2);
  } catch {
    return args;
  }
};

const calcThroughput = (req: RequestStats): number => {
  if (!req.is_completed) return 0;
  const tokens = req.content_tokens + req.reasoning_tokens;
  if (tokens === 0) return 0;
  let genTimeMs = req.e2e_ms - req.ttft_ms;
  if (genTimeMs <= 0 || !req.is_streaming) {
    genTimeMs = req.e2e_ms;
  }
  if (genTimeMs <= 0) return 0;
  return Number(((tokens / genTimeMs) * 1000).toFixed(1));
};

const formatTime = (isoString: string) => {
  if (!isoString) return '--';
  return new Date(isoString).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
};

const getEndTime = (req: RequestStats) => {
  if (!req.is_completed || !req.start_time) return '--';
  const end = new Date(new Date(req.start_time).getTime() + req.e2e_ms);
  return end.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
};

const getCacheHitRate = (req: RequestStats) => {
  if (!req.prompt_tokens || req.prompt_tokens === 0) return '0.0';
  return ((req.cached_tokens / req.prompt_tokens) * 100).toFixed(1);
};

interface UserStats {
  userId: string;
  activeCount: number;
  completedCount: number;
  totalPromptTokens: number;
  totalContentTokens: number;
  totalReasoningTokens: number;
  avgTTFT: number;
  avgE2E: number;
  avgThroughput: number;
  cacheHitRate: string;
}

function App() {
  const [backendUrl, setBackendUrl] = useState(
    () => localStorage.getItem('llminspector_backend_url') ?? 'ws://localhost:8081/ws'
  );
  const [isConnected, setIsConnected] = useState(false);
  const [requests, setRequests] = useState<Map<string, RequestStats>>(new Map());
  const [selectedRequestId, setSelectedRequestId] = useState<string | null>(null);
  const [viewingUser, setViewingUser] = useState<string | null>(null);
  const [isInputCollapsed, setIsInputCollapsed] = useState(true);
  const [isTracking, setIsTracking] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const thinkingRef = useRef<HTMLPreElement>(null);
  const outputRef = useRef<HTMLPreElement>(null);
  const modalBodyRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    connect();
    return () => {
      if (wsRef.current) {
        wsRef.current.close();
      }
    };
  }, [backendUrl]);

  useEffect(() => {
    if (selectedRequestId) {
      setIsInputCollapsed(true);
    }
  }, [selectedRequestId]);

  useEffect(() => {
    if (thinkingRef.current) {
      thinkingRef.current.scrollTop = thinkingRef.current.scrollHeight;
    }
    if (outputRef.current) {
      outputRef.current.scrollTop = outputRef.current.scrollHeight;
    }
    if (modalBodyRef.current) {
      modalBodyRef.current.scrollTop = modalBodyRef.current.scrollHeight;
    }
  }, [requests, selectedRequestId]);

  useEffect(() => {
    if (!isTracking || !viewingUser) return;
    const userReqs = Array.from(requests.values() as Iterable<RequestStats>).filter(r => r.user_id === viewingUser);
    if (userReqs.length === 0) return;
    const latest = userReqs.reduce((a: RequestStats, b: RequestStats) =>
      new Date(a.start_time) >= new Date(b.start_time) ? a : b
    );
    setSelectedRequestId(latest.id);
  }, [requests, isTracking, viewingUser]);

  const connect = () => {
    if (wsRef.current) {
      wsRef.current.close();
    }

    try {
      const ws = new WebSocket(backendUrl);
      
      ws.onopen = async () => {
        setIsConnected(true);
        setRequests(new Map());
        try {
          const httpBase = backendUrl.replace(/^ws(s?):\/\//, 'http$1://').replace(/\/ws$/, '');
          const res = await fetch(`${httpBase}/api/requests`);
          const historical: RequestStats[] = await res.json();
          setRequests(prev => {
            const merged = new Map(historical.map((r: RequestStats) => [r.id, r]));
            for (const [id, req] of prev) merged.set(id, req);
            return merged;
          });
        } catch (e) {
          console.error('Failed to load historical requests', e);
        }
      };

      ws.onmessage = (event) => {
        const data: RequestStats = JSON.parse(event.data);
        setRequests(prev => {
          const newMap = new Map(prev);
          newMap.set(data.id, data);
          return newMap;
        });
      };

      ws.onclose = () => {
        setIsConnected(false);
      };

      ws.onerror = (error) => {
        console.error('WebSocket Error:', error);
      };

      wsRef.current = ws;
    } catch (e) {
      console.error('Connection failed', e);
    }
  };

  // Aggregation for Level 1
  const userStats = useMemo(() => {
    const map = new Map<string, {
      active: number;
      completed: number;
      promptTokens: number;
      contentTokens: number;
      reasoningTokens: number;
      cachedTokens: number;
      totalTTFT: number;
      totalE2E: number;
      totalThroughput: number;
    }>();

    for (const req of requests.values()) {
      const user = req.user_id;
      if (!map.has(user)) {
        map.set(user, { active: 0, completed: 0, promptTokens: 0, contentTokens: 0, reasoningTokens: 0, cachedTokens: 0, totalTTFT: 0, totalE2E: 0, totalThroughput: 0 });
      }
      const stats = map.get(user)!;
      
      stats.promptTokens += req.prompt_tokens || 0;
      stats.contentTokens += req.content_tokens;
      stats.reasoningTokens += req.reasoning_tokens;
      stats.cachedTokens += req.cached_tokens || 0;

      if (req.is_completed) {
        stats.completed += 1;
        stats.totalTTFT += req.ttft_ms;
        stats.totalE2E += req.e2e_ms;
        stats.totalThroughput += calcThroughput(req);
      } else {
        stats.active += 1;
      }
    }

    const result: UserStats[] = [];
    for (const [userId, stats] of map.entries()) {
      result.push({
        userId,
        activeCount: stats.active,
        completedCount: stats.completed,
        totalPromptTokens: stats.promptTokens,
        totalContentTokens: stats.contentTokens,
        totalReasoningTokens: stats.reasoningTokens,
        avgTTFT: stats.completed > 0 ? Number((stats.totalTTFT / stats.completed / 1000).toFixed(2)) : 0,
        avgE2E: stats.completed > 0 ? Number((stats.totalE2E / stats.completed / 1000).toFixed(2)) : 0,
        avgThroughput: stats.completed > 0 ? Number((stats.totalThroughput / stats.completed).toFixed(1)) : 0,
        cacheHitRate: stats.promptTokens > 0 ? ((stats.cachedTokens / stats.promptTokens) * 100).toFixed(1) : '0.0',
      });
    }
    
    // Sort by active count descending, then completed descending
    result.sort((a, b) => {
      if (b.activeCount !== a.activeCount) return b.activeCount - a.activeCount;
      return b.completedCount - a.completedCount;
    });

    return result;
  }, [requests]);

  const renderLevel1 = () => (
    <>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(400px, 1fr))', gap: '1.5rem', marginBottom: '1.5rem' }}>
        <div className="glass-card" style={{ height: '350px', margin: 0, display: 'flex', flexDirection: 'column' }}>
          <h2 style={{ marginTop: 0, color: 'var(--text-secondary)', flexShrink: 0 }}>Global Latency (s)</h2>
          {userStats.length === 0 ? (
            <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No data available</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={userStats} margin={{ top: 20, right: 30, left: 0, bottom: 20 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" vertical={false} />
                <XAxis dataKey="userId" stroke="#64748b" tick={{fontSize: 12}} />
                <YAxis stroke="#64748b" tick={{fontSize: 12}} />
                <Tooltip cursor={{fill: '#f1f5f9'}} contentStyle={{borderRadius: '8px', border: '1px solid #e2e8f0', boxShadow: '0 4px 6px -1px rgba(0,0,0,0.05)'}} />
                <Legend wrapperStyle={{paddingTop: '10px'}} />
                <Bar dataKey="avgTTFT" name="Avg TTFT (s)" fill="#38bdf8" radius={[4, 4, 0, 0]} maxBarSize={40} />
                <Bar dataKey="avgE2E" name="Avg E2E (s)" fill="#818cf8" radius={[4, 4, 0, 0]} maxBarSize={40} />
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>

        <div className="glass-card" style={{ height: '350px', margin: 0, display: 'flex', flexDirection: 'column' }}>
          <h2 style={{ marginTop: 0, color: 'var(--text-secondary)', flexShrink: 0 }}>Global Throughput (Tokens/s)</h2>
          {userStats.length === 0 ? (
            <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No data available</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={userStats} margin={{ top: 20, right: 30, left: 0, bottom: 20 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" vertical={false} />
                <XAxis dataKey="userId" stroke="#64748b" tick={{fontSize: 12}} />
                <YAxis stroke="#64748b" tick={{fontSize: 12}} />
                <Tooltip cursor={{fill: '#f1f5f9'}} contentStyle={{borderRadius: '8px', border: '1px solid #e2e8f0', boxShadow: '0 4px 6px -1px rgba(0,0,0,0.05)'}} />
                <Legend wrapperStyle={{paddingTop: '10px'}} />
                <Bar dataKey="avgThroughput" name="Avg TPS" fill="#10b981" radius={[4, 4, 0, 0]} maxBarSize={40} />
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>
      </div>

      <div className="glass-card" style={{ marginTop: '1.5rem' }}>
        <h2 style={{ marginTop: 0, color: 'var(--text-secondary)' }}>Users Overview</h2>
      {userStats.length === 0 ? (
        <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No data available</div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th>User ID</th>
                <th>Active</th>
                <th>Completed</th>
                <th>Tokens (Prompt / Content / Reasoning)</th>
                <th>Avg TTFT (s)</th>
                <th>Avg E2E (s)</th>
                <th>Avg TPS</th>
                <th>Cache Hit Rate</th>
              </tr>
            </thead>
            <tbody>
              {userStats.map(user => (
                <tr key={user.userId} className="clickable-row" onClick={() => setViewingUser(user.userId)}>
                  <td style={{ fontWeight: 600, color: 'var(--text-primary)' }}>{user.userId}</td>
                  <td><span className={user.activeCount > 0 ? 'badge' : ''}>{user.activeCount}</span></td>
                  <td>{user.completedCount}</td>
                  <td>{user.totalPromptTokens} / {user.totalContentTokens} / {user.totalReasoningTokens}</td>
                  <td>{user.avgTTFT}s</td>
                  <td>{user.avgE2E}s</td>
                  <td><span style={{ color: '#10b981', fontWeight: 600 }}>{user.avgThroughput}</span></td>
                  <td><span style={{ color: '#8b5cf6', fontWeight: 600 }}>{user.cacheHitRate}%</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
    </>
  );

  const renderLevel2 = () => {
    const userReqs = Array.from(requests.values()).filter(r => r.user_id === viewingUser);
    const activeReqs = userReqs.filter(r => !r.is_completed);
    const completedReqs = userReqs.filter(r => r.is_completed);

    const totalTokens = completedReqs.reduce((acc, r) => acc + r.content_tokens + r.reasoning_tokens, 0);
    const avgTTFT = completedReqs.length > 0 
      ? Number((completedReqs.reduce((acc, r) => acc + r.ttft_ms, 0) / completedReqs.length / 1000).toFixed(2))
      : 0;

    const avgTPS = completedReqs.length > 0 
      ? Number((completedReqs.reduce((acc, r) => acc + calcThroughput(r), 0) / completedReqs.length).toFixed(1))
      : 0;

    const totalUserPromptTokens = userReqs.reduce((acc, r) => acc + (r.prompt_tokens || 0), 0);
    const totalUserCachedTokens = userReqs.reduce((acc, r) => acc + (r.cached_tokens || 0), 0);
    const avgCacheHitRate = totalUserPromptTokens > 0 ? ((totalUserCachedTokens / totalUserPromptTokens) * 100).toFixed(1) : '0.0';

    const chartData = completedReqs.map(req => ({
      time: new Date(req.start_time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
      ttft: Number((req.ttft_ms / 1000).toFixed(2)),
      e2e: Number((req.e2e_ms / 1000).toFixed(2)),
      throughput: calcThroughput(req)
    })).slice(-50); // Show last 50 points

    return (
      <>
        <div className="sub-header" style={{ display: 'flex', alignItems: 'center', gap: '1rem', marginBottom: '1.5rem' }}>
          <button className="back-btn" onClick={() => { setViewingUser(null); setIsTracking(false); setSelectedRequestId(null); }}>← Back</button>
          <h2 style={{ margin: 0, color: 'var(--text-primary)' }}>Requests for <span style={{ color: 'var(--accent-primary)' }}>{viewingUser}</span></h2>
          <button
            onClick={() => setIsTracking(t => !t)}
            style={{
              marginLeft: 'auto',
              padding: '0.4rem 1rem',
              borderRadius: '999px',
              border: isTracking ? 'none' : '1px solid var(--glass-border)',
              background: isTracking ? 'linear-gradient(135deg, #ef4444, #f97316)' : 'transparent',
              color: isTracking ? '#fff' : 'var(--text-secondary)',
              fontWeight: 600,
              fontSize: '0.875rem',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: '0.4rem',
              transition: 'all 0.2s',
            }}
          >
            <span style={{ display: 'inline-block', width: '8px', height: '8px', borderRadius: '50%', background: isTracking ? '#fff' : 'var(--text-secondary)', animation: isTracking ? 'pulse 1s infinite' : 'none' }} />
            {isTracking ? 'Tracking...' : 'Track Latest'}
          </button>
        </div>

        <div className="stats-grid">
          <div className="glass-card">
            <div className="stat-label">Active</div>
            <div className="stat-value">{activeReqs.length}</div>
          </div>
          <div className="glass-card">
            <div className="stat-label">Completed</div>
            <div className="stat-value">{completedReqs.length}</div>
          </div>
          <div className="glass-card">
            <div className="stat-label">Avg TTFT</div>
            <div className="stat-value">{avgTTFT}s</div>
          </div>
          <div className="glass-card">
            <div className="stat-label">Total Tokens</div>
            <div className="stat-value">{totalTokens}</div>
          </div>
          <div className="glass-card">
            <div className="stat-label">Avg TPS</div>
            <div className="stat-value" style={{color: '#10b981'}}>{avgTPS}</div>
          </div>
          <div className="glass-card">
            <div className="stat-label">Cache Hit Rate</div>
            <div className="stat-value" style={{color: '#8b5cf6'}}>{avgCacheHitRate}%</div>
          </div>
        </div>

        <div className="glass-card" style={{ marginBottom: '1.5rem', height: '350px', display: 'flex', flexDirection: 'column' }}>
          <h2 style={{ marginTop: 0, color: 'var(--text-secondary)', flexShrink: 0 }}>Latency & Throughput Trend</h2>
          {chartData.length === 0 ? (
            <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No completed requests to graph</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartData} margin={{ top: 20, right: 30, left: 0, bottom: 20 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" vertical={false} />
                <XAxis dataKey="time" stroke="#64748b" tick={{fontSize: 12}} />
                <YAxis yAxisId="left" stroke="#64748b" tick={{fontSize: 12}} />
                <YAxis yAxisId="right" orientation="right" stroke="#10b981" tick={{fontSize: 12}} />
                <Tooltip contentStyle={{borderRadius: '8px', border: '1px solid #e2e8f0', boxShadow: '0 4px 6px -1px rgba(0,0,0,0.05)'}} />
                <Legend wrapperStyle={{paddingTop: '10px'}} />
                <Line yAxisId="left" type="monotone" dataKey="ttft" name="TTFT (s)" stroke="#38bdf8" strokeWidth={2} dot={{r: 3}} activeDot={{r: 6}} />
                <Line yAxisId="left" type="monotone" dataKey="e2e" name="E2E (s)" stroke="#818cf8" strokeWidth={2} dot={{r: 3}} activeDot={{r: 6}} />
                <Line yAxisId="right" type="monotone" dataKey="throughput" name="Throughput (t/s)" stroke="#10b981" strokeWidth={2} dot={{r: 3}} activeDot={{r: 6}} />
              </LineChart>
            </ResponsiveContainer>
          )}
        </div>

        <div className="grid" style={{ display: 'block' }}>
          <div className="glass-card" style={{ marginBottom: '1.5rem' }}>
            <h2 style={{ marginTop: 0, color: 'var(--text-secondary)' }}>Active Streams</h2>
            {activeReqs.length === 0 ? (
              <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No active requests</div>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table>
                  <thead>
                    <tr>
                      <th>Start Time</th>
                      <th>Model</th>
                      <th>Tokens (Prompt / Content / Reasoning)</th>
                      <th>TTFT (s)</th>
                      <th>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {activeReqs.map(req => (
                      <tr key={req.id} className="clickable-row" onClick={() => setSelectedRequestId(req.id)}>
                        <td style={{ color: 'var(--text-secondary)' }}>{formatTime(req.start_time)}</td>
                        <td>
                          <span className="badge">{req.model || 'unknown'}</span>
                        </td>
                        <td>
                          <div>
                            {req.prompt_tokens} / {req.is_thinking ? (
                              <span style={{ color: '#475569', fontWeight: 600 }}>{req.reasoning_tokens}</span>
                            ) : (
                              <span>{req.content_tokens}</span>
                            )} / {req.reasoning_tokens}
                          </div>
                          {req.cached_tokens > 0 && (
                            <div style={{ color: '#10b981', fontSize: '0.8em', marginTop: '0.25rem', fontWeight: 600 }}>
                              ⚡ Cache: {req.cached_tokens} ({getCacheHitRate(req)}%)
                            </div>
                          )}
                          {parseToolCalls(req.tool_calls).length > 0 && (
                            <div style={{ color: '#f59e0b', fontSize: '0.8em', marginTop: '0.25rem', fontWeight: 600 }}>
                              🔧 {parseToolCalls(req.tool_calls).length} tool call(s)
                            </div>
                          )}
                        </td>
                        <td>{req.ttft_ms ? `${(req.ttft_ms / 1000).toFixed(2)}s` : 'waiting...'}</td>
                        <td>
                          <span className={`badge ${req.is_thinking ? 'thinking' : 'outputting'}`}>
                            {req.is_thinking ? 'Thinking...' : 'Outputting...'}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="glass-card">
            <h2 style={{ marginTop: 0, color: 'var(--text-secondary)' }}>Completed Requests</h2>
            {completedReqs.length === 0 ? (
              <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No completed requests yet</div>
            ) : (
              <div style={{ overflowX: 'auto' }}>
                <table>
                  <thead>
                    <tr>
                      <th>Start Time</th>
                      <th>End Time</th>
                      <th>Model</th>
                      <th>Tokens (Prompt / Content / Reasoning)</th>
                      <th>TTFT (s)</th>
                      <th>E2E (s)</th>
                      <th>TPS</th>
                      <th>Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {completedReqs.slice(-20).reverse().map(req => (
                      <tr key={req.id} className="clickable-row" onClick={() => setSelectedRequestId(req.id)}>
                        <td style={{ color: 'var(--text-secondary)' }}>{formatTime(req.start_time)}</td>
                        <td style={{ color: 'var(--text-secondary)' }}>{getEndTime(req)}</td>
                        <td>
                          <span className="badge">{req.model || 'unknown'}</span>
                        </td>
                        <td>
                          <div>
                            {req.prompt_tokens} / {req.content_tokens} / {req.reasoning_tokens}
                          </div>
                          {req.cached_tokens > 0 && (
                            <div style={{ color: '#10b981', fontSize: '0.8em', marginTop: '0.25rem', fontWeight: 600 }}>
                              ⚡ Cache: {req.cached_tokens} ({getCacheHitRate(req)}%)
                            </div>
                          )}
                          {parseToolCalls(req.tool_calls).length > 0 && (
                            <div style={{ color: '#f59e0b', fontSize: '0.8em', marginTop: '0.25rem', fontWeight: 600 }}>
                              🔧 {parseToolCalls(req.tool_calls).length} tool call(s)
                            </div>
                          )}
                        </td>
                        <td>{(req.ttft_ms / 1000).toFixed(2)}s</td>
                        <td>{(req.e2e_ms / 1000).toFixed(2)}s</td>
                        <td><span style={{ color: '#10b981' }}>{calcThroughput(req)}</span></td>
                        <td><span className="badge completed">Done</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      </>
    );
  };

  const overallActive = Array.from(requests.values()).filter(r => !r.is_completed).length;
  const overallCompleted = Array.from(requests.values()).filter(r => r.is_completed).length;

  return (
    <div className="dashboard-container">
      <header className="header">
        <h1>LLM Inspector</h1>
        <div className="connection-status">
          <div className={`status-dot ${isConnected ? 'connected' : ''}`}></div>
          {isConnected ? 'Connected' : 'Disconnected'}
        </div>
      </header>

      <div className="setup-form glass-card" style={{ display: viewingUser ? 'none' : 'flex' }}>
        <input 
          type="text" 
          value={backendUrl} 
          onChange={(e) => {
            setBackendUrl(e.target.value);
            localStorage.setItem('llminspector_backend_url', e.target.value);
          }}
          placeholder="ws://localhost:8081/ws"
        />
        <button onClick={connect}>Connect</button>
      </div>

      {viewingUser === null ? (
        <>
          <div className="stats-grid">
            <div className="glass-card">
              <div className="stat-label">Total Users</div>
              <div className="stat-value">{userStats.length}</div>
            </div>
            <div className="glass-card">
              <div className="stat-label">Total Active</div>
              <div className="stat-value">{overallActive}</div>
            </div>
            <div className="glass-card">
              <div className="stat-label">Total Completed</div>
              <div className="stat-value">{overallCompleted}</div>
            </div>
          </div>
          {renderLevel1()}
        </>
      ) : (
        renderLevel2()
      )}

      {selectedRequestId && requests.get(selectedRequestId) && (
        <div className="modal-overlay" onClick={() => { setSelectedRequestId(null); setIsTracking(false); }}>
          <div className="modal-content" onClick={e => e.stopPropagation()}>
            <div className="modal-header" style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', width: '100%', alignItems: 'center' }}>
                <h2>Request Details <span className="badge">{requests.get(selectedRequestId)!.model}</span></h2>
                <button className="close-btn" onClick={() => { setSelectedRequestId(null); setIsTracking(false); }}>×</button>
              </div>
              <div style={{ display: 'flex', gap: '1rem', color: 'var(--text-secondary)', fontSize: '0.875rem', marginTop: '0.5rem', flexWrap: 'wrap' }}>
                <span><strong>Start:</strong> {formatTime(requests.get(selectedRequestId)!.start_time)}</span>
                {requests.get(selectedRequestId)!.is_completed && (
                  <>
                    <span><strong>End:</strong> {getEndTime(requests.get(selectedRequestId)!)}</span>
                    <span><strong>TTFT:</strong> {(requests.get(selectedRequestId)!.ttft_ms / 1000).toFixed(2)}s</span>
                    <span><strong>E2E:</strong> {(requests.get(selectedRequestId)!.e2e_ms / 1000).toFixed(2)}s</span>
                    <span><strong>TPS:</strong> {calcThroughput(requests.get(selectedRequestId)!)}</span>
                    <span><strong>Total Tokens:</strong> {requests.get(selectedRequestId)!.total_tokens}</span>
                    {requests.get(selectedRequestId)!.cached_tokens > 0 && (
                      <span style={{ color: '#10b981', fontWeight: 600 }}><strong>⚡ Cache:</strong> {requests.get(selectedRequestId)!.cached_tokens} ({getCacheHitRate(requests.get(selectedRequestId)!)}%)</span>
                    )}
                  </>
                )}
                {!requests.get(selectedRequestId)!.is_completed && (
                  <span><strong>Status:</strong> <span style={{ color: requests.get(selectedRequestId)!.is_thinking ? '#8b5cf6' : '#38bdf8' }}>{requests.get(selectedRequestId)!.is_thinking ? 'Thinking...' : 'Streaming...'}</span></span>
                )}
              </div>
            </div>
            <div className="modal-body" ref={modalBodyRef}>
              <div className="content-section">
                <h3 
                  style={{ display: 'flex', alignItems: 'center', cursor: 'pointer', gap: '0.5rem' }} 
                  onClick={() => setIsInputCollapsed(!isInputCollapsed)}
                >
                  <span style={{ fontSize: '0.8em', color: 'var(--text-secondary)' }}>
                    {isInputCollapsed ? '▶' : '▼'}
                  </span>
                  Input JSON
                </h3>
                {!isInputCollapsed && (
                  <pre className="code-block">{requests.get(selectedRequestId)!.input_content || 'No input content available.'}</pre>
                )}
              </div>
              
              {requests.get(selectedRequestId)!.reasoning_content && (
                <div className="content-section">
                  <h3>Thinking Process</h3>
                  <pre className="code-block" style={{ color: '#475569' }} ref={thinkingRef}>
                    {requests.get(selectedRequestId)!.reasoning_content}
                  </pre>
                </div>
              )}

              <div className="content-section">
                <h3>Real-time Output</h3>
                <pre className="code-block" ref={outputRef}>
                  {requests.get(selectedRequestId)!.output_content || 'Waiting for output...'}
                </pre>
              </div>

              {(() => {
                const toolCalls = parseToolCalls(requests.get(selectedRequestId)!.tool_calls);
                if (toolCalls.length === 0) return null;
                return (
                  <div className="content-section">
                    <h3>Tool Calls <span style={{ fontSize: '0.85em', color: 'var(--text-secondary)', fontWeight: 400 }}>({toolCalls.length})</span></h3>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                      {toolCalls.map((tc, i) => (
                        <div key={tc.id || i} style={{
                          border: '1px solid #fde68a',
                          borderRadius: '8px',
                          overflow: 'hidden',
                          background: 'rgba(251, 191, 36, 0.05)',
                        }}>
                          <div style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '0.5rem',
                            padding: '0.5rem 0.75rem',
                            background: 'rgba(251, 191, 36, 0.1)',
                            borderBottom: '1px solid #fde68a',
                          }}>
                            <span style={{ fontSize: '0.85em' }}>🔧</span>
                            <span style={{ fontWeight: 700, color: '#b45309', fontFamily: 'monospace', fontSize: '0.9em' }}>
                              {tc.function.name}
                            </span>
                            {tc.id && (
                              <span style={{ marginLeft: 'auto', color: 'var(--text-secondary)', fontSize: '0.75em', fontFamily: 'monospace' }}>
                                {tc.id}
                              </span>
                            )}
                          </div>
                          <pre className="code-block" style={{ margin: 0, borderRadius: 0, borderTop: 'none', fontSize: '0.85em', maxHeight: '200px', overflowY: 'auto' }}>
                            {formatToolArguments(tc.function.arguments) || '(no arguments)'}
                          </pre>
                        </div>
                      ))}
                    </div>
                  </div>
                );
              })()}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export default App;
