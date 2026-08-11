import { FormEvent, useEffect, useRef, useState } from 'react';
import './console.css';

type Message = { type: string; stream?: string; data?: string; state?: string };
const maxLines = 2000;

export function ConsoleTab({ serverID, serverState, canSend = true }: { serverID: string; serverState: string; canSend?: boolean }) {
  const [lines, setLines] = useState<Message[]>([]);
  const [status, setStatus] = useState('connecting');
  const [command, setCommand] = useState('');
  const [auto, setAuto] = useState(true);
  const socket = useRef<WebSocket | null>(null);
  const view = useRef<HTMLDivElement>(null);
  const disabled = !canSend || serverState === 'stopped' || serverState === 'crashed' || status === 'detached' || status === 'closed';
  useEffect(() => { let closed = false, tries = 0, timer = 0; const connect = () => { const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/v1/servers/${serverID}/console/ws`); socket.current = ws; ws.onopen = () => { tries = 0; setStatus('connected'); setLines([]); }; ws.onmessage = e => { try { const m = JSON.parse(e.data) as Message; if (m.type === 'console') setStatus(m.state ?? 'closed'); else setLines(v => [...v, m].slice(-maxLines)); } catch {} }; ws.onclose = () => { if (!closed && tries < 5) { setStatus('disconnected'); timer = window.setTimeout(connect, Math.min(1000 * 2 ** tries++, 10000)); } }; }; connect(); return () => { closed = true; window.clearTimeout(timer); socket.current?.close(); }; }, [serverID]);
  useEffect(() => { if (auto) view.current?.scrollTo({ top: view.current.scrollHeight }); }, [lines, auto]);
  function send(e: FormEvent) { e.preventDefault(); if (!command || disabled || socket.current?.readyState !== WebSocket.OPEN) return; socket.current.send(JSON.stringify({ type: 'input', data: command.endsWith('\n') ? command : command + '\n' })); setCommand(''); }
  if (status === 'detached') return <section className="console-panel"><h3>Console</h3><p className="muted">Console unavailable because this process was rediscovered after GameNode restarted. Restart the server through GameNode to restore console attachment.</p></section>;
  return <section className="console-panel"><div className="row"><h3>Console</h3><span className="muted">{status} · {serverState}{!canSend && ' · read-only'}</span></div><div className="console-output" ref={view}>{lines.map((l, i) => <div key={i} className={`console-line ${l.stream ?? ''}`}>{l.data ?? l.state ?? ''}</div>)}</div><div className="console-controls"><label><input type="checkbox" checked={auto} onChange={e => setAuto(e.target.checked)} /> Auto-scroll</label><button type="button" className="quiet" onClick={() => setLines([])}>Clear view</button></div><form className="console-input" onSubmit={send}><input value={command} onChange={e => setCommand(e.target.value)} disabled={disabled} placeholder={disabled ? (canSend ? 'Console input unavailable' : 'Read-only console') : 'Enter command'} /><button disabled={disabled || !command}>Send</button></form></section>;
}
