import React, { useState, useEffect, useRef, useCallback } from "react";
import "../styles/ai.css";

interface Message {
  role: "user" | "assistant";
  content: string;
  model?: string;
  provider?: string;
  tokensIn?: number;
  tokensOut?: number;
  iterations?: number;
  loading?: boolean;
}

interface MemoryEntry {
  key: string;
  value: string;
  updatedAt: string;
}

const API_BASE = import.meta.env.VITE_API_BASE ?? "";

async function sendChat(message: string, model: string) {
  const res = await fetch(`${API_BASE}/api/ai/chat`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, model }),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json() as Promise<{
    reply: string; model: string; provider: string;
    tokensIn: number; tokensOut: number; iterations: number;
  }>;
}

async function fetchMemory(): Promise<MemoryEntry[]> {
  const res = await fetch(`${API_BASE}/api/ai/memory`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

async function importMemory(entries: MemoryEntry[]): Promise<void> {
  await fetch(`${API_BASE}/api/ai/memory`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(entries),
  });
}

async function deleteMemoryKey(key: string): Promise<void> {
  await fetch(`${API_BASE}/api/ai/memory?key=${encodeURIComponent(key)}`, { method: "DELETE" });
}

async function deleteAllMemory(): Promise<void> {
  await fetch(`${API_BASE}/api/ai/memory`, { method: "DELETE" });
}

function ProviderBadge({ provider, model }: { provider: string; model: string }) {
  return (
    <span className={`ai-badge ai-badge--${provider}`}>
      {provider} · {model}
    </span>
  );
}

function TokenBadge({ tokIn, tokOut }: { tokIn: number; tokOut: number }) {
  return (
    <span className="ai-token-badge">
      <span className="ai-token-in">&#8593;{tokIn}</span>{" "}
      <span className="ai-token-out">&#8595;{tokOut}</span>
    </span>
  );
}

function MessageBubble({ msg }: { msg: Message }) {
  if (msg.role === "user") {
    return (
      <div className="ai-bubble ai-bubble--user">
        <div className="ai-bubble__content">{msg.content}</div>
      </div>
    );
  }
  return (
    <div className="ai-bubble ai-bubble--assistant">
      {msg.loading ? (
        <div className="ai-bubble__content ai-bubble__loading">
          <span className="ai-dot" /><span className="ai-dot" /><span className="ai-dot" />
        </div>
      ) : (
        <>
          <div className="ai-bubble__content">{msg.content}</div>
          {msg.provider && msg.model && (
            <div className="ai-bubble__meta">
              <ProviderBadge provider={msg.provider} model={msg.model} />
              {msg.tokensIn != null && msg.tokensOut != null && (
                <TokenBadge tokIn={msg.tokensIn} tokOut={msg.tokensOut} />
              )}
              {msg.iterations != null && msg.iterations > 1 && (
                <span className="ai-iter-badge">{msg.iterations} iters</span>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function MemoryPanel({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [entries, setEntries] = useState<MemoryEntry[]>([]);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try { setEntries(await fetchMemory()); }
    catch (e) { console.error("fetchMemory:", e); }
    finally { setLoading(false); }
  }, []);

  useEffect(() => { if (open) load(); }, [open, load]);

  const handleExport = () => {
    const blob = new Blob([JSON.stringify(entries, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url; a.download = "agent-memory.json"; a.click();
    URL.revokeObjectURL(url);
  };

  const handleImport = () => {
    const input = document.createElement("input");
    input.type = "file"; input.accept = ".json";
    input.onchange = async (e) => {
      const file = (e.target as HTMLInputElement).files?.[0];
      if (!file) return;
      try {
        const data = JSON.parse(await file.text()) as MemoryEntry[];
        await importMemory(data); load();
      } catch { alert("Invalid JSON file"); }
    };
    input.click();
  };

  const handleDeleteAll = async () => {
    if (!confirm("Delete ALL memory facts?")) return;
    await deleteAllMemory(); load();
  };

  const handleDeleteKey = async (key: string) => {
    await deleteMemoryKey(key);
    setEntries((prev) => prev.filter((e) => e.key !== key));
  };

  if (!open) return null;

  return (
    <div className="ai-memory-panel">
      <div className="ai-memory-panel__header">
        <span className="ai-memory-panel__title">Agent Memory</span>
        <div className="ai-memory-panel__toolbar">
          <button className="ai-memory-btn" onClick={handleExport}>Export</button>
          <button className="ai-memory-btn" onClick={handleImport}>Import</button>
          <button className="ai-memory-btn ai-memory-btn--danger" onClick={handleDeleteAll}>Delete all</button>
          <button className="ai-memory-btn ai-memory-btn--close" onClick={onClose}>x</button>
        </div>
      </div>
      {loading ? (
        <div className="ai-memory-panel__empty">Loading...</div>
      ) : entries.length === 0 ? (
        <div className="ai-memory-panel__empty">No facts remembered yet.</div>
      ) : (
        <ul className="ai-memory-list">
          {entries.map((e) => (
            <li key={e.key} className="ai-memory-item">
              <span className="ai-memory-item__key">{e.key}</span>
              <span className="ai-memory-item__value">{e.value}</span>
              <button className="ai-memory-item__delete" onClick={() => handleDeleteKey(e.key)}>x</button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export default function AIAssistantPage() {
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [model, setModel] = useState("claude-sonnet-4-5");
  const [sending, setSending] = useState(false);
  const [memoryOpen, setMemoryOpen] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => { bottomRef.current?.scrollIntoView({ behavior: "smooth" }); }, [messages]);

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || sending) return;
    setInput(""); setSending(true);
    setMessages((prev) => [...prev,
      { role: "user", content: text },
      { role: "assistant", content: "", loading: true },
    ]);
    try {
      const data = await sendChat(text, model);
      setMessages((prev) => [...prev.slice(0, -1), {
        role: "assistant", content: data.reply,
        model: data.model, provider: data.provider,
        tokensIn: data.tokensIn, tokensOut: data.tokensOut, iterations: data.iterations,
      }]);
    } catch (e) {
      setMessages((prev) => [...prev.slice(0, -1), {
        role: "assistant",
        content: `Error: ${e instanceof Error ? e.message : String(e)}`,
      }]);
    } finally { setSending(false); }
  }, [input, model, sending]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
  };

  return (
    <div className="ai-page">
      <header className="ai-header">
        <span className="ai-header__title">AI Assistant</span>
        <div className="ai-header__controls">
          <input
            className="ai-model-input" value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="model name" title="Model override"
          />
          <button className="ai-memory-toggle" onClick={() => setMemoryOpen((o) => !o)}>
            Memory
          </button>
        </div>
      </header>

      <MemoryPanel open={memoryOpen} onClose={() => setMemoryOpen(false)} />

      <main className="ai-messages">
        {messages.length === 0 && (
          <div className="ai-messages__empty">
            Start a conversation. The agent remembers facts across sessions.
          </div>
        )}
        {messages.map((msg, i) => <MessageBubble key={i} msg={msg} />)}
        <div ref={bottomRef} />
      </main>

      <footer className="ai-input-area">
        <textarea
          className="ai-input" value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Message... (Enter to send, Shift+Enter for newline)"
          rows={3} disabled={sending}
        />
        <button className="ai-send-btn" onClick={send} disabled={sending || !input.trim()}>
          {sending ? "..." : "Send"}
        </button>
      </footer>
    </div>
  );
}
