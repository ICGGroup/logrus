import React, { useState, useEffect, useRef, useMemo } from 'react';
import { Play, Pause, Trash2, Search, X, Zap, Server } from 'lucide-react';

// Main App Component
const App = () => {
  // --- STATE MANAGEMENT ---
  const [sseUrl, setSseUrl] = useState('http://localhost:8080/events');
  const [isConnected, setIsConnected] = useState(false);
  const [events, setEvents] = useState([]);
  const [typeFilter, setTypeFilter] = useState('');
  const [dataFilter, setDataFilter] = useState('');
  const [expandedEvents, setExpandedEvents] = useState({});
  const eventSourceRef = useRef(null);

  // --- LOCAL STORAGE CACHING ---
  // Load events from localStorage on initial render
  useEffect(() => {
    try {
      const cachedEvents = localStorage.getItem('sse-events-cache');
      if (cachedEvents) {
        setEvents(JSON.parse(cachedEvents));
      }
    } catch (error) {
      console.error("Failed to load events from localStorage", error);
      localStorage.removeItem('sse-events-cache');
    }
  }, []);

  // Save events to localStorage whenever they change
  useEffect(() => {
    try {
      localStorage.setItem('sse-events-cache', JSON.stringify(events));
    } catch (error) {
      console.error("Failed to save events to localStorage", error);
    }
  }, [events]);


  // --- SSE CONNECTION LOGIC ---
  const handleConnect = () => {
    if (!sseUrl) return;

    const eventSource = new EventSource(sseUrl);
    eventSourceRef.current = eventSource;

    eventSource.onopen = () => {
      console.log("SSE connection established.");
      setIsConnected(true);
    };

    eventSource.onmessage = (event) => {
      try {
        const parsedEvent = JSON.parse(event.data);
        // Add a unique local ID for React keys and timestamp for sorting
        parsedEvent._localId = `${Date.now()}-${parsedEvent.id}`;
        parsedEvent._receivedAt = new Date().toISOString();
        setEvents(prevEvents => [parsedEvent, ...prevEvents]);
      } catch (error) {
        console.error("Failed to parse incoming event data:", error);
      }
    };
    
    // This handles custom named events from the eventbus
    eventSource.addEventListener('log.entry.v1.unknown', (e) => {
        console.log('Received custom event:', e);
    });

    eventSource.onerror = (err) => {
      console.error("EventSource failed:", err);
      eventSource.close();
      setIsConnected(false);
    };
  };

  const handleDisconnect = () => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
      eventSourceRef.current = null;
      setIsConnected(false);
      console.log("SSE connection closed.");
    }
  };
  
  const toggleConnection = () => {
    if (isConnected) {
      handleDisconnect();
    } else {
      handleConnect();
    }
  };
  
  // Cleanup on component unmount
  useEffect(() => {
    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }
    };
  }, []);

  // --- FILTERING LOGIC ---
  const filteredEvents = useMemo(() => {
    return events.filter(event => {
      const typeMatch = typeFilter ? event.type?.toLowerCase().includes(typeFilter.toLowerCase()) : true;
      let dataMatch = true;
      if (dataFilter) {
          try {
              // Generic search on the stringified data object
              const dataString = JSON.stringify(event.data).toLowerCase();
              dataMatch = dataString.includes(dataFilter.toLowerCase());
          } catch {
              dataMatch = false;
          }
      }
      return typeMatch && dataMatch;
    });
  }, [events, typeFilter, dataFilter]);
  
  // --- UI HANDLERS ---
  const clearEvents = () => {
    setEvents([]);
    setExpandedEvents({});
  };
  
  const toggleExpandEvent = (id) => {
    setExpandedEvents(prev => ({ ...prev, [id]: !prev[id] }));
  };

  return (
    <div className="flex flex-col h-screen bg-gray-900 text-gray-200 font-sans">
      <Header />
      <div className="flex-grow flex p-4 gap-4 overflow-hidden">
        {/* Left Panel: Controls */}
        <div className="w-1/3 max-w-sm flex flex-col gap-4">
          <ConnectionForm
            sseUrl={sseUrl}
            setSseUrl={setSseUrl}
            isConnected={isConnected}
            toggleConnection={toggleConnection}
            clearEvents={clearEvents}
            eventCount={events.length}
          />
          <FilterControls
            typeFilter={typeFilter}
            setTypeFilter={setTypeFilter}
            dataFilter={dataFilter}
            setDataFilter={setDataFilter}
          />
        </div>
        
        {/* Right Panel: Event List */}
        <div className="flex-grow flex flex-col bg-gray-800 rounded-lg shadow-inner overflow-hidden">
          <EventListHeader count={filteredEvents.length} />
          <EventList 
            events={filteredEvents} 
            expandedEvents={expandedEvents} 
            toggleExpandEvent={toggleExpandEvent} 
          />
        </div>
      </div>
    </div>
  );
};

// --- SUB-COMPONENTS ---

const Header = () => (
  <header className="flex-shrink-0 bg-gray-800/50 backdrop-blur-sm border-b border-gray-700 p-3 flex items-center justify-between shadow-md">
    <div className="flex items-center gap-3">
      <Zap className="text-blue-400 w-7 h-7" />
      <h1 className="text-xl font-bold text-gray-100 tracking-wider">Event Bus Inspector</h1>
    </div>
  </header>
);

const ConnectionForm = ({ sseUrl, setSseUrl, isConnected, toggleConnection, clearEvents, eventCount }) => (
  <div className="bg-gray-800 p-4 rounded-lg shadow-lg">
    <h2 className="text-lg font-semibold mb-3 text-gray-300 flex items-center gap-2"><Server className="w-5 h-5 text-gray-400"/>Connection</h2>
    <div className="flex gap-2">
      <input
        type="text"
        value={sseUrl}
        onChange={(e) => setSseUrl(e.target.value)}
        placeholder="Enter SSE Stream URL"
        disabled={isConnected}
        className="flex-grow bg-gray-700 border border-gray-600 rounded-md px-3 py-2 text-gray-200 focus:ring-2 focus:ring-blue-500 focus:outline-none transition-shadow"
      />
      <button 
        onClick={toggleConnection}
        className={`px-4 py-2 rounded-md font-semibold text-white transition-all flex items-center gap-2 shadow-sm ${isConnected ? 'bg-red-600 hover:bg-red-700' : 'bg-blue-600 hover:bg-blue-700'}`}
      >
        {isConnected ? <Pause size={18} /> : <Play size={18} />}
        {isConnected ? 'Disconnect' : 'Connect'}
      </button>
    </div>
    <div className="mt-3 flex justify-between items-center">
        <span className="text-sm text-gray-400">Cached Events: {eventCount}</span>
        <button 
            onClick={clearEvents}
            className="px-3 py-1 rounded-md text-xs font-semibold text-gray-300 bg-gray-700 hover:bg-gray-600 transition-colors flex items-center gap-1"
        >
            <Trash2 size={14}/> Clear
        </button>
    </div>
  </div>
);

const FilterControls = ({ typeFilter, setTypeFilter, dataFilter, setDataFilter }) => (
    <div className="bg-gray-800 p-4 rounded-lg shadow-lg flex-grow">
      <h2 className="text-lg font-semibold mb-3 text-gray-300 flex items-center gap-2"><Search className="w-5 h-5 text-gray-400"/>Filters</h2>
      <div className="space-y-3">
        <div className="relative">
          <input
            type="text"
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            placeholder="Filter by Event Type (e.g., log.entry)"
            className="w-full bg-gray-700 border border-gray-600 rounded-md px-3 py-2 text-gray-200 focus:ring-2 focus:ring-blue-500 focus:outline-none transition-shadow"
          />
          {typeFilter && <X onClick={() => setTypeFilter('')} className="absolute right-3 top-2.5 w-5 h-5 text-gray-500 hover:text-gray-300 cursor-pointer"/>}
        </div>
        <div className="relative">
          <input
            type="text"
            value={dataFilter}
            onChange={(e) => setDataFilter(e.target.value)}
            placeholder="Filter by any text in Event Data"
            className="w-full bg-gray-700 border border-gray-600 rounded-md px-3 py-2 text-gray-200 focus:ring-2 focus:ring-blue-500 focus:outline-none transition-shadow"
          />
          {dataFilter && <X onClick={() => setDataFilter('')} className="absolute right-3 top-2.5 w-5 h-5 text-gray-500 hover:text-gray-300 cursor-pointer"/>}
        </div>
      </div>
    </div>
);

const EventListHeader = ({ count }) => (
    <div className="flex-shrink-0 p-3 bg-gray-900/50 border-b border-gray-700">
        <h3 className="font-semibold text-gray-300">Event Stream ({count} matching)</h3>
    </div>
);

const EventList = ({ events, expandedEvents, toggleExpandEvent }) => (
  <div className="flex-grow overflow-y-auto p-2 space-y-2">
    {events.length === 0 ? (
      <div className="flex items-center justify-center h-full text-gray-500">
        Waiting for events...
      </div>
    ) : (
      events.map(event => (
        <EventItem 
          key={event._localId} 
          event={event}
          isExpanded={expandedEvents[event._localId] || false}
          onToggleExpand={() => toggleExpandEvent(event._localId)}
        />
      ))
    )}
  </div>
);

const EventItem = ({ event, isExpanded, onToggleExpand }) => {
    const level = event.data?.level?.toLowerCase() || 'default';
    const levelColorClasses = {
        'error': 'border-red-500/50 bg-red-900/20',
        'warn': 'border-yellow-500/50 bg-yellow-900/20',
        'info': 'border-blue-500/50 bg-blue-900/20',
        'debug': 'border-cyan-500/50 bg-cyan-900/20',
        'trace': 'border-purple-500/50 bg-purple-900/20',
        'default': 'border-gray-600 bg-gray-800/20',
    };

    return (
        <div className={`border-l-4 rounded-md shadow-md transition-colors ${levelColorClasses[level]}`}>
            <div 
                className="p-3 cursor-pointer bg-gray-800 hover:bg-gray-700/50"
                onClick={onToggleExpand}
            >
                <div className="flex justify-between items-start">
                    <div className="flex-grow truncate mr-4">
                        <span className="font-mono text-xs text-gray-400 mr-2">{new Date(event._receivedAt).toLocaleTimeString()}</span>
                        <span className="font-semibold text-gray-200">{event.data?.message || 'Event Received'}</span>
                    </div>
                    <div className="flex-shrink-0 text-right">
                        <span className="font-mono text-xs px-2 py-1 rounded bg-gray-700 text-gray-300">{event.type}</span>
                    </div>
                </div>
            </div>
            {isExpanded && (
                <div className="p-3 bg-gray-800/50 border-t border-gray-700">
                    <pre className="text-xs text-gray-400 whitespace-pre-wrap break-all bg-gray-900/50 p-3 rounded-md">
                        {JSON.stringify(event, null, 2)}
                    </pre>
                </div>
            )}
        </div>
    );
};

export default App;
