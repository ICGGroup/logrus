import React, { useState, useEffect, useRef } from 'react';
import './App.css';
import { downloadJSON, downloadNDJSON } from './utils';

const LEVEL_COLORS = {
  error: '#ff4d4f',
  warning: '#faad14',
  info: '#1890ff',
  debug: '#52c41a',
};

function App() {
  const [events, setEvents] = useState([]);
  const [filter, setFilter] = useState('');
  const [connected, setConnected] = useState(false);
  const [expanded, setExpanded] = useState({});
  const [maxLines, setMaxLines] = useState(20000);
  const [darkMode, setDarkMode] = useState(false);
  const eventSourceRef = useRef(null);
  const socketRef = useRef(null);


  const matchFilter = (data, query) => {
    if (!query) return true;

    const evalExpr = (obj, expr) => {
      console.log('Evaluating expression:', expr, 'on data:', obj);
      const parts = expr.split(/\s+(and|or)\s+/);
      let result = null;

      for (let i = 0; i < parts.length; i += 2) {
        const condition = parts[i];

        const match = condition.trim().match(/^(!)?(.+?)(=|!=|:|!:)"(.*?)"$/);
        if (!match) continue;

        const [_, not, path, op, value] = match;
        const keys = path.split('.');
        let cur = obj;
        for (const k of keys) {
          if (typeof cur !== 'object' || cur === null) {
            cur = undefined;
            break;
          }
          cur = cur[k];
        }

        let ok = false;
        if (op === '=') ok = cur == value;
        else if (op === '!=') ok = cur != value;
        else if (op === ':') ok = typeof cur === 'string' && cur.startsWith(value);
        else if (op === '!:') ok = typeof cur === 'string' && !cur.startsWith(value);

        if (not) ok = !ok;

        if (result === null) result = ok;
        else {
          const logic = parts[i - 1];
          result = logic === 'and' ? result && ok : result || ok;
        }
      }

      return result;
    };

    return evalExpr(data, query);
  };

  const applyFilter = (event) => {
    return matchFilter(event.data, filter);
  };

  function handleEvent(evt, type) {
    try {
      const e = JSON.parse(evt.data || evt);
      e._eventType = type || evt.type || 'unknown';
      setEvents(prev => [e, ...prev.slice(0, maxLines - 1)]);
    } catch (err) {
      console.error('Failed to parse event', err);
    }
  }

  const connectSSE = () => {
    if (eventSourceRef.current) eventSourceRef.current.close();
    const source = new EventSource('/events');
    eventSourceRef.current = source;
    source.onopen = () => setConnected(true);
    source.onerror = () => {
      setConnected(false);
      connectWebSocket();
    };
    source.addEventListener('github.com.icgdevspace.logevent', handleEvent);
  };

  const connectWebSocket = () => {
    const socket = new WebSocket('ws://' + window.location.host + '/ws');
    socketRef.current = socket;
    socket.onmessage = (e) => handleEvent(e, 'websocket');
    socket.onopen = () => setConnected(true);
    socket.onerror = () => setConnected(false);
  };

  const toggleExpand = (id) => {
    setExpanded(prev => ({ ...prev, [id]: !prev[id] }));
  };

  const toggleDarkMode = () => {
    setDarkMode(!darkMode);
    document.body.classList.toggle('dark-mode', !darkMode);
  };

  return (
    <div className="app-container">
      <h2>CloudEvents Viewer</h2>
      <div className="toolbar">
        <button onClick={connectSSE} disabled={connected}>{connected ? 'Connected' : 'Connect to SSE'}</button>
        <input
          placeholder='Filter (e.g. data.callSign="Flounder" or data.module:="github")'
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <input
          type="number"
          min={100}
          value={maxLines}
          onChange={(e) => setMaxLines(parseInt(e.target.value) || 20000)}
          title="Max history lines"
        />
        
        <button onClick={() => downloadJSON('events.json', events.filter(applyFilter))}>Export JSON</button>
        <button onClick={() => downloadNDJSON('events.ndjson', events.filter(applyFilter))}>Export NDJSON</button>
        {/*<button onClick={toggleDarkMode}>{darkMode ? 'Light Mode' : 'Dark Mode'}</button>*/}

      </div>
      <div className="examples">
        <span>Examples:</span>
        <code>{'{"level":"error"}'}</code>
        <code>{'!{"level":"info"}'}</code>
        <code>{'log.github.com.icggroup'}</code>
        <code>{'!log.main'}</code>
      </div>
      <div className="log-container">
        {events.filter(applyFilter).map((event, i) => {
          const data = event.data || {};
          const level = data.level?.toLowerCase() || 'info';
          const color = LEVEL_COLORS[level] || '#ccc';
          return (
            <div
              key={event.id + i}
              className="log-row"
              style={{ borderLeft: `4px solid ${color}` }}
              onClick={() => toggleExpand(event.id)}
            >
              <div className="log-header">
                <span className="time">{new Date(event.time || event.timestamp).toLocaleTimeString()}</span>
                <span className="level" style={{ color }}>{level.toUpperCase()}</span>
                <span className="message">{data.message}</span>
                <span className="module">[{data.module}]</span>
              </div>
              {expanded[event.id] && (
                <pre className="log-details">{JSON.stringify(data, null, 2)}</pre>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export default App;
