package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/browser"
	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
)

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'%3E%3Ctext y='.9em' font-size='90'%3E%F0%9F%94%90%3C/text%3E%3C/svg%3E">
<title>🔐 tantu hub</title>
<style>
  :root {
    --bg: #090b10;
    --card-bg: #131722;
    --card-hover: #181d2b;
    --surface: #0f1420;
    --surface-hover: #1a2233;
    --border: #232a3b;
    --border-light: #323c52;
    --text: #e6edf3;
    --text-muted: #8b949e;
    --accent: #3b82f6;
    --accent-hover: #2563eb;
    --accent-gradient: linear-gradient(135deg, #3b82f6, #6366f1);
    --success: #10b981;
    --success-bg: rgba(16, 185, 129, 0.12);
    --success-text: #34d399;
    --warning: #f59e0b;
    --warning-bg: rgba(245, 158, 11, 0.12);
    --warning-text: #fbbf24;
    --error: #ef4444;
    --error-bg: rgba(239, 68, 68, 0.12);
    --error-text: #f87171;
    --purple: #a855f7;
    --radius-sm: 6px;
    --radius-md: 8px;
    --radius-lg: 12px;
    --radius-pill: 9999px;
    --shadow-sm: 0 1px 2px rgba(0, 0, 0, 0.3);
    --shadow-md: 0 4px 20px rgba(0, 0, 0, 0.3);
    --shadow-lg: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
    --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    --font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    --ease-out: cubic-bezier(0.2, 0, 0, 1);
    --dur-fast: 150ms;
    --dur-med: 250ms;
    color-scheme: dark;
  }
  /* Header and tab-bar chrome stay dark in both OS themes by design: it reads
     as an intentional app frame, and the alternative churned every token in
     both palettes. Text sitting on that dark chrome must therefore not
     inherit the light-theme body colour; see .version-tag. */
  @media (prefers-color-scheme: light) {
    :root {
      --bg: #f4f6fb;
      --card-bg: #ffffff;
      --card-hover: #f0f4fa;
      --surface: #eef1f7;
      --surface-hover: #e2e8f2;
      --border: #d9e0ec;
      --border-light: #b9c4d8;
      --text: #16213a;
      --text-muted: #5b6478;
      --accent: #2563eb;
      --accent-hover: #1d4ed8;
      --accent-gradient: linear-gradient(135deg, #2563eb, #4f46e5);
      --success: #047857;
      --success-bg: rgba(4, 120, 87, 0.1);
      /* #047857 on this tint measured 4.24:1 at 12px; deepening the text
         clears 4.5:1 while staying the same hue. */
      --success-text: #065f46;
      --warning: #b45309;
      --warning-bg: rgba(180, 83, 9, 0.1);
      /* Deepened from #b45309, which measured 3.9:1 on the warning tint at
         12px. Warning chips are a real state (retryable, duplicate risk), not
         decoration, so they have to be readable. */
      --warning-text: #7c3d06;
      --error: #dc2626;
      --error-bg: rgba(220, 38, 38, 0.08);
      --error-text: #b91c1c;
      --purple: #7e22ce;
      --shadow-sm: 0 1px 2px rgba(22, 33, 58, 0.08);
      --shadow-md: 0 4px 20px rgba(22, 33, 58, 0.1);
      --shadow-lg: 0 25px 50px -12px rgba(22, 33, 58, 0.25);
      color-scheme: light;
    }
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    min-height: 100vh;
    display: flex;
    flex-direction: column;
  }
  header {
    background: rgba(19, 23, 34, 0.85);
    backdrop-filter: blur(12px);
    border-bottom: 1px solid var(--border);
    padding: 1rem 2rem;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
    position: sticky;
    top: 0;
    z-index: 50;
  }
  .logo-group {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
    flex-shrink: 1;
  }
  .logo-title {
    font-size: 1.25rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    background: var(--accent-gradient);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }
  .version-tag {
    font-size: 0.75rem;
    /* Declared here, not earlier: the version tag sits on the deliberately
       dark header chrome in both themes, so it must not inherit the light
       theme's muted body colour (that pairing measured 1.69:1). */
    color: #b8c4d6;
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid rgba(255, 255, 255, 0.12);
    padding: 0.15rem 0.45rem;
    border-radius: 9999px;
  }
  .peer-status-pill {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    background: var(--card-bg);
    border: 1px solid var(--border);
    padding: 0.4rem 0.85rem;
    border-radius: 9999px;
    font-size: 0.85rem;
  }
  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--success);
    box-shadow: 0 0 8px var(--success);
  }
  .status-dot.offline {
    background: var(--warning);
    box-shadow: 0 0 8px var(--warning);
  }
  nav.tabs-nav {
    padding: 1rem 2rem 0;
    border-bottom: 1px solid var(--border);
    background: rgba(19, 23, 34, 0.4);
  }
  .tab-btn {
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    font-size: 0.95rem;
    font-weight: 600;
    padding: 0.75rem 1.25rem;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    transition: all 0.2s;
  }
  .tab-btn:hover {
    color: var(--text);
  }
  .tab-btn.active {
    color: var(--accent);
    border-bottom-color: transparent;
  }
  .tab-btn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
    border-radius: 6px;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
  .drop-zone:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 3px;
  }
  main.tab-content {
    flex: 1;
    padding: 2rem;
    max-width: 1000px;
    margin: 0 auto;
    width: 100%;
  }
  .tab-pane {
    display: none;
  }
  .tab-pane.active {
    display: block;
    animation: fadeIn 0.2s ease-in-out;
  }
  @keyframes fadeIn {
    from { opacity: 0; transform: translateY(4px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 1.5rem;
    margin-bottom: 1.5rem;
    box-shadow: 0 4px 20px rgba(0, 0, 0, 0.3);
  }
  .card-title {
    font-size: 1.1rem;
    font-weight: 600;
    margin-bottom: 1rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .drop-zone {
    border: 2px dashed var(--border-light);
    border-radius: 12px;
    padding: 2.5rem 1rem;
    text-align: center;
    background: rgba(255, 255, 255, 0.01);
    cursor: pointer;
    transition: all 0.2s;
  }
  .drop-zone.dragover {
    border-color: var(--accent);
    background: rgba(59, 130, 246, 0.08);
  }
  .drop-zone-icon {
    font-size: 2.5rem;
    margin-bottom: 0.75rem;
  }
  .drop-zone-text {
    font-size: 1rem;
    color: var(--text-muted);
  }
  .drop-zone-subtext {
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-top: 0.35rem;
  }
  .progress-container {
    margin-top: 1rem;
    display: none;
  }
  .progress-bar {
    height: 8px;
    background: rgba(255, 255, 255, 0.08);
    border-radius: 9999px;
    overflow: hidden;
  }
  .progress-fill {
    height: 100%;
    width: 0%;
    background: var(--accent-gradient);
    transition: width 0.2s;
  }
  .progress-meta {
    display: flex;
    justify-content: space-between;
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-top: 0.4rem;
  }
  .form-group {
    display: flex;
    gap: 0.75rem;
    margin-top: 1rem;
  }
  input[type="text"], textarea {
    width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 0.75rem 1rem;
    color: var(--text);
    font-size: 0.95rem;
    outline: none;
    transition: border-color var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out);
  }
  input[type="text"]:focus, textarea:focus {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.15);
  }
  textarea {
    min-height: 100px;
    resize: vertical;
    font-family: inherit;
  }
  .btn-primary {
    background: var(--accent-gradient);
    border: none;
    color: #fff;
    padding: 0.75rem 1.5rem;
    border-radius: 8px;
    font-weight: 600;
    font-size: 0.95rem;
    cursor: pointer;
    transition: opacity 0.2s;
    white-space: nowrap;
  }
  .btn-primary:hover {
    opacity: 0.9;
  }
  .btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .btn-sm {
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 0.35rem 0.75rem;
    border-radius: 6px;
    font-size: 0.8rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s;
  }
  .btn-sm:hover {
    background: rgba(255, 255, 255, 0.12);
    border-color: var(--border-light);
  }
  .received-item {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 0.85rem;
    box-shadow: var(--shadow-sm);
    transition: border-color var(--dur-fast) var(--ease-out);
  }
  .received-item:hover {
    border-color: var(--border-light);
  }
  .received-item-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 0.8rem;
    color: var(--text-muted);
    margin-bottom: 0.5rem;
  }
  .received-item-content {
    background: var(--surface);
    border: 1px solid rgba(127, 140, 160, 0.18);
    border-radius: var(--radius-sm);
    padding: 0.6rem 0.75rem;
    font-family: var(--font-mono);
    font-size: 0.85rem;
    color: var(--text);
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 160px;
    overflow-y: auto;
  }
  .received-item-preview {
    max-width: 100%;
    max-height: 320px;
    border-radius: 8px;
    border: 1px solid rgba(255,255,255,0.08);
    object-fit: contain;
  }
  .received-item-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .bookmarklet-box {
    background: rgba(59, 130, 246, 0.06);
    border: 1px dashed var(--accent);
    border-radius: 12px;
    padding: 1.5rem;
    text-align: center;
    margin-bottom: 1.5rem;
  }
  .bookmarklet-btn {
    display: inline-block;
    background: var(--accent-gradient);
    color: #fff;
    text-decoration: none;
    font-weight: 600;
    padding: 0.75rem 1.5rem;
    border-radius: 9999px;
    box-shadow: 0 4px 14px rgba(59, 130, 246, 0.4);
    cursor: move;
  }
  .hint-text {
    font-size: 0.85rem;
    color: var(--text-muted);
    margin-top: 0.75rem;
  }
  .grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5rem;
  }
  /* Grid and flex items default to min-width:auto, so a single wide descendant
     (a long path, a long peer name, an unbreakable string) forces the track
     past its container and pushes the whole page into horizontal scroll. These
     are the two-column layouts and their column wrappers. */
  .grid-2 > * { min-width: 0; }
  .grid-2 .card,
  .grid-2 .card > * { min-width: 0; }
  @media (max-width: 768px) {
    .grid-2 { grid-template-columns: 1fr; }
    .tabs-list { overflow-x: auto; }
  }
  .stat-row {
    display: flex;
    justify-content: space-between;
    padding: 0.6rem 0;
    border-bottom: 1px solid rgba(255, 255, 255, 0.05);
    font-size: 0.9rem;
  }
  .stat-label { color: var(--text-muted); }
  .stat-val { font-family: monospace; font-weight: 600; }
  .stat-val.mono-accent { color: #1d4ed8; }
  @media (prefers-color-scheme: dark) {
    .stat-val.mono-accent { color: #93c5fd; }
  }
  .log-console {
    background: #06070a;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 1rem;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.85rem;
    height: 380px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .log-entry {
    line-height: 1.4;
    word-break: break-all;
  }
  .tag {
    display: inline-block;
    padding: 0.1rem 0.4rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-weight: 700;
    margin-right: 0.5rem;
  }
  /* Domain tags serve two surfaces. Inside the log console they are terminal
     chips on a deliberately dark background, so they keep their fixed
     console palette in both themes. As standalone badges (the Default and
     Active markers on a peer row) they sit on an ordinary themed card, where
     the console palette measured 1.67:1 in the light theme. The two cases are
     therefore scoped separately. */
  .tag-oauth { background: rgba(59, 130, 246, 0.2); color: var(--accent); }
  .tag-drop { background: var(--success-bg); color: var(--success-text); }
  .tag-peer { background: rgba(168, 85, 247, 0.2); color: #7e22ce; }
  .tag-net { background: var(--warning-bg); color: var(--warning-text); }
  .tag-sys { background: rgba(6, 182, 212, 0.2); color: #0e7490; }
  .tag-debug { background: rgba(127, 140, 160, 0.16); color: var(--text-muted); }
  .tag-error { background: var(--error-bg); color: var(--error-text); }
  .log-console .tag-oauth { color: #60a5fa; }
  .log-console .tag-drop { color: #34d399; }
  .log-console .tag-peer { color: #c084fc; }
  .log-console .tag-net { color: #fbbf24; }
  .log-console .tag-sys { color: #22d3ee; }
  .log-console .tag-debug { background: rgba(255, 255, 255, 0.08); color: #94a3b8; }
  .log-console .tag-error { color: #f87171; }
  @media (prefers-color-scheme: dark) {
    .tag-peer { color: #c084fc; }
    .tag-sys { color: #22d3ee; }
  }
  .pill {
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid var(--border);
    color: var(--text-muted);
    padding: 0.25rem 0.65rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s;
  }
  .pill:hover {
    background: rgba(255, 255, 255, 0.1);
    color: var(--text);
  }
  .pill.active {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  /* White on --accent measured 3.68:1 at 12px, under AA for small text.
     Deepening the active fill clears 4.5:1 without changing the hue. */
  .pill.active { background: var(--accent-hover); }
  .status-banner {
    display: none;
    border-radius: 8px;
    padding: 0.85rem 1rem;
    margin-top: 1rem;
    font-size: 0.9rem;
  }
  .status-banner.success { display: block; background: var(--success-bg); border: 1px solid var(--success); color: #34d399; }
  .status-banner.error { display: block; background: var(--error-bg); border: 1px solid var(--error); color: #f87171; }
  .status-banner.relaying { display: block; background: var(--warning-bg); border: 1px solid var(--warning); color: #fbbf24; }
  [id] { scroll-margin-top: calc(var(--chrome-h, 129px) + 12px); }
  #nextActionBanner {
    display: none;
    background: rgba(59,130,246,0.08);
    border: 1px solid rgba(59,130,246,0.4);
    color: var(--banner-text, #bfdbfe);
    border-radius: 12px;
    padding: 0.85rem 1.25rem;
    margin-bottom: 1.25rem;
    font-size: 0.9rem;
  }
  /* #bfdbfe measured 1.2:1 on the light page; guidance must be readable
     because it is the only thing telling a new user what to do next. */
  @media (prefers-color-scheme: light) {
    #nextActionBanner {
      --banner-text: #123a7a;
      background: rgba(59, 130, 246, 0.12);
      border-color: rgba(37, 99, 235, 0.45);
    }
  }
  #destinationSummary {
    font-size: 0.85rem;
    color: var(--text);
    background: rgba(59,130,246,0.08);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.5rem 0.75rem;
    margin-bottom: 0.75rem;
  }
  #filePreviewCard {
    border: 1px solid var(--accent);
    background: rgba(59,130,246,0.06);
    border-radius: 12px;
    padding: 1rem 1.25rem;
    margin-top: 1rem;
  }
  #filePreviewCard img {
    max-width: 100%;
    max-height: 240px;
    border-radius: 8px;
    border: 1px solid rgba(255,255,255,0.08);
    object-fit: contain;
    display: block;
    margin-bottom: 0.75rem;
  }
  @media (prefers-reduced-motion: reduce) {
    *, *::before, *::after {
      animation-duration: 0.01ms !important;
      animation-iteration-count: 1 !important;
      transition-duration: 0.01ms !important;
    }
  }
  /* ---- Premium layer: elevation, motion, and component polish ---- */
  body {
    font-feature-settings: "cv11";
    -webkit-font-smoothing: antialiased;
  }
  header {
    box-shadow: var(--shadow-sm);
  }
  .logo-title {
    letter-spacing: -0.03em;
  }
  .card {
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-md);
    transition: border-color var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out);
  }
  .peer-status-pill {
    box-shadow: var(--shadow-sm);
    border-radius: var(--radius-pill);
  }
  /* The peer select is sized by its longest option. A long peer name therefore
     used to widen the whole header and push the page into horizontal scroll at
     narrow widths, on every tab. It is capped, allowed to shrink, and given an
     explicit ellipsis so a long name truncates visually while the accessible
     name keeps the full value. */
  .peer-status-pill {
    min-width: 0;
    flex-shrink: 1;
  }
  /* The <select> is nested inside the label span, so the span is the flex item
     that has to be allowed to shrink; without min-width:0 it kept the width of
     its longest option and pushed the header past the viewport. */
  .peer-status-pill #peerLabel {
    min-width: 0;
    overflow: hidden;
  }
  .peer-status-pill select {
    font: inherit;
    max-width: 16rem;
    min-width: 0;
    text-overflow: ellipsis;
  }
  .tab-btn {
    position: relative;
    border-radius: 8px 8px 0 0;
  }
  .tab-btn::after {
    content: "";
    position: absolute;
    left: 0.75rem;
    right: 0.75rem;
    bottom: -1px;
    height: 2px;
    border-radius: 2px;
    background: transparent;
    transition: background var(--dur-fast) var(--ease-out);
  }
  .tab-btn.active::after {
    background: var(--accent);
  }
  .drop-zone {
    border-radius: var(--radius-lg);
    transition: border-color var(--dur-med) var(--ease-out), background var(--dur-med) var(--ease-out), transform var(--dur-med) var(--ease-out);
  }
  .drop-zone.dragover {
    transform: scale(1.01);
    box-shadow: 0 0 0 4px rgba(59, 130, 246, 0.15);
  }
  .progress-fill {
    transition: width var(--dur-fast) linear;
  }
  .progress-meta {
    font-variant-numeric: tabular-nums;
  }
  .btn-primary {
    border-radius: var(--radius-md);
    box-shadow: 0 4px 14px rgba(59, 130, 246, 0.35);
    transition: transform var(--dur-fast) var(--ease-out), opacity var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out);
  }
  .btn-primary:hover:not(:disabled) {
    transform: translateY(-1px);
    box-shadow: 0 6px 18px rgba(59, 130, 246, 0.45);
  }
  .btn-primary:active:not(:disabled) {
    transform: translateY(0);
  }
  .btn-sm {
    border-radius: var(--radius-sm);
    transition: background var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
  }
  .status-banner {
    border-radius: var(--radius-md);
    animation: bannerIn var(--dur-med) var(--ease-out);
  }
  @keyframes bannerIn {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }
  .status-banner.success { color: var(--success-text); }
  .status-banner.error { color: var(--error-text); }
  .status-banner.relaying { color: var(--warning-text); }
  .status-chip {
    display: inline-block;
    padding: 0.15rem 0.6rem;
    border-radius: var(--radius-pill);
    font-size: 0.75rem;
    font-weight: 600;
    border: 1px solid transparent;
    white-space: nowrap;
  }
  .status-chip.ok { background: var(--success-bg); border-color: var(--success); color: var(--success-text); }
  .status-chip.warn { background: var(--warning-bg); border-color: var(--warning); color: var(--warning-text); }
  .status-chip.err { background: var(--error-bg); border-color: var(--error); color: var(--error-text); }
  .status-chip.info { background: rgba(59, 130, 246, 0.1); border-color: var(--accent); color: var(--accent); }
  .status-chip.neutral { background: rgba(139, 148, 158, 0.12); border-color: var(--border-light); color: var(--text-muted); }
  .record-state { margin-top: 0.25rem; }
  .record-next { font-size: 0.8rem; color: var(--text-muted); margin-top: 0.25rem; }
  /* Monospace accent chip. #60a5fa on white measured 2.27:1 and 2.54:1, so
     the accent text steps down in the light theme while keeping the tint. */
  .mono-chip {
    font-family: var(--font-mono);
    font-size: 0.85rem;
    padding: 0.2rem 0.5rem;
    border-radius: var(--radius-sm);
    background: rgba(59, 130, 246, 0.1);
    color: #1d4ed8;
    overflow-wrap: anywhere;
    word-break: break-word;
  }
  @media (prefers-color-scheme: dark) {
    .mono-chip { color: #93c5fd; }
  }
  .received-item {
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-sm);
  }
  .received-item-preview, #filePreviewThumb {
    opacity: 0;
    transition: opacity var(--dur-med) var(--ease-out);
  }
  .received-item-preview.loaded, #filePreviewThumb.loaded {
    opacity: 1;
  }
  .log-console {
    border-radius: var(--radius-md);
    font-variant-numeric: tabular-nums;
  }
  .empty-state {
    color: var(--text-muted);
    font-size: 0.85rem;
    text-align: center;
    padding: 2rem 0;
  }
  .empty-state strong {
    display: block;
    color: var(--text);
    font-size: 0.95rem;
    margin-bottom: 0.25rem;
  }
  input[type="text"]:focus-visible, textarea:focus-visible, select:focus-visible, button:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .modal-overlay {
    background: rgba(4, 6, 12, 0.7);
    animation: fadeIn var(--dur-fast) var(--ease-out);
  }
  .modal-box {
    background: var(--card-bg);
    border: 1px solid var(--border);
    color: var(--text);
    animation: modalIn var(--dur-med) var(--ease-out);
  }
  @media (prefers-color-scheme: light) {
    .modal-overlay { background: rgba(22, 33, 58, 0.45); }
  }
  .pair-title { margin: 0; font-size: 1.1rem; color: var(--text); }
  .pair-close { background: none; border: none; color: var(--text-muted); font-size: 1.4rem; cursor: pointer; line-height: 1; border-radius: var(--radius-sm); }
  .pair-close:hover { color: var(--text); }
  .pair-text { font-size: 0.85rem; color: var(--text-muted); margin-bottom: 1.25rem; }
  .pair-sas-box { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 1rem; margin-bottom: 1.25rem; text-align: center; }
  .pair-sas-label { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-muted); margin-bottom: 0.25rem; }
  .pair-sas-code { font-family: var(--font-mono); font-size: 1.5rem; font-weight: 700; color: var(--accent); letter-spacing: 0.1em; }
  .pair-opt-label { display: block; font-size: 0.8rem; font-weight: 600; color: var(--text); margin-bottom: 0.5rem; }
  .pair-cmd { flex: 1; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0.5rem 0.75rem; font-size: 0.8rem; color: var(--accent); overflow-x: auto; white-space: nowrap; font-family: var(--font-mono); }
  @keyframes modalIn {
    from { opacity: 0; transform: translateY(8px) scale(0.98); }
    to { opacity: 1; transform: translateY(0) scale(1); }
  }
  .bookmarklet-box {
    border-radius: var(--radius-lg);
  }
  .bookmarklet-btn {
    transition: transform var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out);
  }
  .bookmarklet-btn:hover {
    transform: translateY(-1px);
  }
  #destinationSummary {
    border-radius: var(--radius-md);
    border-left: 3px solid var(--accent);
  }
  #filePreviewCard {
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-md);
  }
  #nextActionBanner {
    border-radius: var(--radius-lg);
    animation: bannerIn var(--dur-med) var(--ease-out);
  }
  #sessionBanner {
    border-radius: var(--radius-lg);
  }
  .stat-val {
    font-variant-numeric: tabular-nums;
  }
  @media (max-width: 640px) {
    header { padding: 0.75rem 1rem; }
    nav.tabs-nav { padding: 0.75rem 1rem 0; }
    main.tab-content { padding: 1rem; }
    .card { padding: 1rem; }
    .form-group { flex-direction: column; }
    .btn-primary { width: 100%; }
    .peer-status-pill select { max-width: 9rem; }
  }
  /* ---- Accessibility mechanics ---- */
  /* Bypass block: the five tabs are the first focusable things on the page. */
  .skip-link {
    position: absolute;
    left: -9999px;
    top: 0;
    z-index: 10000;
    background: var(--card-bg);
    color: var(--text);
    border: 1px solid var(--accent);
    border-radius: 0 0 var(--radius-md) 0;
    padding: 0.75rem 1rem;
    font-weight: 600;
    text-decoration: none;
  }
  .skip-link:focus {
    left: 0;
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  main:focus { outline: none; }
  /* The tab list scrolls inside the navigation landmark rather than replacing
     it, so a screen reader still gets banner / navigation / main. */
  .tabs-list {
    display: flex;
    gap: 0.5rem;
    overflow-x: auto;
    scrollbar-width: thin;
  }
  @media (max-width: 768px) {
    .tabs-list { overflow-x: auto; }
  }
  /* The log console is a terminal surface and stays dark in both OS themes by
     design. It therefore owns its text colours instead of inheriting body
     colour, which flips to a dark navy in the light theme and made the whole
     console unreadable there. */
  .log-console {
    color: #d7dde8;
  }
  .log-console .log-time { color: #8b98ad; }
  /* Inbound pairing approval: the only action that grants permanent mutual
     trust, so it gets a real heading, theme-aware classes, and full-size
     targets instead of hardcoded inline colours. */
  .pairing-banner {
    background: var(--warning-bg);
    border: 1px solid var(--warning);
    border-radius: var(--radius-lg);
    padding: 1.25rem;
    margin-bottom: 1.5rem;
  }
  .pairing-banner-title {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    color: var(--warning-text);
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .pairing-banner-badge {
    font-size: 0.75rem;
    background: var(--warning-bg);
    border: 1px solid var(--warning);
    color: var(--warning-text);
    padding: 0.2rem 0.6rem;
    border-radius: var(--radius-pill);
    font-weight: 600;
    white-space: nowrap;
  }
  .pairing-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 0.75rem 1rem;
    flex-wrap: wrap;
    gap: 0.75rem;
  }
  .pairing-row-name { font-weight: 600; color: var(--text); }
  .pairing-row-addr { font-size: 0.8rem; color: var(--text-muted); font-weight: 400; }
  .pairing-row-meta { font-size: 0.85rem; color: var(--text-muted); margin-top: 0.25rem; }
  .pairing-sas {
    color: var(--accent);
    font-family: var(--font-mono);
    font-size: 1.05rem;
    letter-spacing: 0.05em;
    background: rgba(59, 130, 246, 0.12);
    padding: 0.15rem 0.5rem;
    border-radius: var(--radius-sm);
  }
  .pairing-actions { display: flex; gap: 0.5rem; }
  .btn-approve, .btn-reject {
    min-height: 44px;
    padding: 0.5rem 1.1rem;
    font-weight: 600;
    border-radius: var(--radius-md);
    cursor: pointer;
    color: #fff;
  }
  .btn-approve { background: var(--success); border: 1px solid var(--success); }
  .btn-reject { background: var(--error); border: 1px solid var(--error); }
  /* Dismiss control of a modal dialog: a 13x22 hit area is not a target. */
  /* 44px is the measured target including the border box, so the floor is
     declared slightly higher to survive sub-pixel layout rounding. */
  .pair-close { min-width: 46px; min-height: 46px; text-align: center; }
  .btn-approve, .btn-reject { min-height: 46px; }
  /* Filter and destination pills: target size is met with spacing rather than
     by inflating every pill to full height, which the diagnostics density
     does not need. */
  .pill { min-height: 32px; }
  .log-filters { gap: 0.6rem; }
  /* Destructive actions are marked visually as well as confirmed, so the
     three Clear buttons do not read as neutral housekeeping. */
  .btn-destructive {
    color: var(--error-text);
    border-color: var(--error);
    background: var(--error-bg);
  }
  .btn-destructive:hover {
    background: var(--error-bg);
    border-color: var(--error);
    color: var(--error-text);
  }
</style>
</head>
<body>
  <a class="skip-link" href="#main">Skip to main content</a>
  <h1 class="sr-only">Tantu Hub dashboard</h1>
  <header>
    <div class="logo-group">
      <div class="logo-title">🔐 tantu</div>
      <span class="version-tag">v{{VERSION}}</span>
    </div>
    <div class="peer-status-pill">
      <div class="status-dot" id="peerDot"></div>
      <span id="peerLabel">Auto-detecting peer...</span>
    </div>
  </header>

  <nav class="tabs-nav" aria-label="Dashboard sections">
    <div class="tabs-list" role="tablist" aria-label="Dashboard sections">
      <button id="tab-btn-drop" class="tab-btn active" role="tab" aria-selected="true" aria-controls="tab-drop" data-tab="tab-drop" tabindex="0">📦 QuickDrop</button>
      <button id="tab-btn-relay" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-relay" data-tab="tab-relay" tabindex="-1">⚡ OAuth Relay</button>
      <button id="tab-btn-peers" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-peers" data-tab="tab-peers" tabindex="-1">🔗 Peers & Network</button>
      <button id="tab-btn-transfers" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-transfers" data-tab="tab-transfers" tabindex="-1">🔄 Transfers</button>
      <button id="tab-btn-logs" class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-logs" data-tab="tab-logs" tabindex="-1">📋 Live Logs</button>
    </div>
  </nav>

  <main class="tab-content" id="main" tabindex="-1">
    <div id="sessionBanner" role="alert" style="display: none; background: rgba(245,158,11,0.12); border: 1px solid rgba(245,158,11,0.4); color: #fbbf24; border-radius: 12px; padding: 0.85rem 1.25rem; margin-bottom: 1.25rem; font-size: 0.9rem;">
      ⚠️ <strong>Dashboard session not established.</strong>
      <span>Actions on this page will fail until you reconnect. Reopen the dashboard from the Hub terminal (press <kbd>o</kbd>) or reload the page the Hub opened for you.</span>
    </div>
    <!-- INBOUND PAIRING APPROVAL BANNER: the only action in the product that
         grants permanent mutual trust, so it is an announced status region
         with theme-aware classes and full-size targets. -->
    <div id="pendingPairingsBanner" class="pairing-banner" role="status" aria-live="polite" style="display: none;">
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem; flex-wrap: wrap; gap: 0.5rem;">
        <h2 class="pairing-banner-title"><span aria-hidden="true">🔐</span> Incoming pairing request</h2>
        <span class="pairing-banner-badge">Action required</span>
      </div>
      <div id="pendingPairingsList" style="display: flex; flex-direction: column; gap: 0.75rem;"></div>
    </div>
    <div id="nextActionBanner" role="status" aria-live="polite" style="display: none;"></div>

    <!-- TAB 1: QUICKDROP -->
    <div id="tab-drop" class="tab-pane active" role="tabpanel" aria-labelledby="tab-btn-drop">
      <!-- Download Location Bar -->
      <div class="card" style="margin-bottom: 1.25rem; padding: 0.75rem 1.25rem;">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem;">
          <div style="display: flex; align-items: center; gap: 0.5rem; font-size: 0.9rem;">
            <span style="color: var(--text-muted);">📁 Downloads location:</span>
            <code id="downloadDirPath" class="mono-chip">...</code>
          </div>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="change-download-dir">✏️ Change</button>
            <button class="btn-sm" data-action="open-folder">📂 Open Folder</button>
          </div>
        </div>
      </div>

      <!-- Shared send destination: one visible control governing file,
           image, clipboard, and text sends alike. -->
      <div class="card" style="margin-bottom: 1.25rem; padding: 0.75rem 1.25rem;">
        <div id="dropPeerSelector" style="display:none; align-items: center; gap: 0.5rem; flex-wrap: wrap; margin-bottom: 0.5rem;">
          <span style="font-size: 0.8rem; color: var(--text-muted); font-weight: 600;">Destination:</span>
          <div id="dropPeerPills" style="display: flex; gap: 0.4rem; flex-wrap: wrap;"></div>
        </div>
        <div id="destinationSummary" role="status" aria-live="polite">Destination: checking…</div>
      </div>

      <!-- Responsive Dual-Pane Layout -->
      <div class="grid-2">
        <!-- Left Pane: Outbound Sending -->
        <div style="display: flex; flex-direction: column; gap: 1.25rem;">
          <div class="card">
            <h2 class="card-title">📤 Send File or Image (up to 5GB)</h2>
            <div class="drop-zone" id="dropZone" data-action="choose-file" tabindex="0" role="button" aria-label="Choose a file to send to your peer">
              <div class="drop-zone-icon">📁</div>
              <div class="drop-zone-text">Drag & drop files here, or click to browse</div>
              <div class="drop-zone-subtext" id="dropZoneTransportNote">Direct encrypted peer-to-peer streaming • no intermediate server</div>
            </div>
            <input type="file" id="fileInput" aria-label="File to send to your peer" style="display: none;">
            <div id="filePreviewCard" hidden>
              <h3 class="card-title" id="filePreviewTitle">Ready to send</h3>
              <img id="filePreviewThumb" alt="">
              <div id="filePreviewMeta" style="font-size: 0.85rem; color: var(--text-muted); margin-bottom: 0.75rem;"></div>
              <div id="filePreviewDest" style="font-size: 0.85rem; margin-bottom: 0.75rem;"></div>
              <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
                <button type="button" class="btn-primary" id="btnConfirmSend" data-action="confirm-send">Send</button>
                <button type="button" class="btn-sm" id="btnCancelPreview" data-action="cancel-preview">Cancel</button>
              </div>
            </div>
            <label style="display: flex; gap: 0.5rem; align-items: center; font-size: 0.8rem; color: var(--text-muted); margin-top: 0.75rem;">
              <input type="checkbox" id="expertImmediateSend"> Expert immediate-send for this session (skip preview; destination stays visible)
            </label>
            
            <div class="progress-container" id="uploadProgress">
              <div class="progress-bar" role="progressbar" aria-label="File upload progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0" aria-describedby="progressFile" id="progressBar">
                <div class="progress-fill" id="progressFill"></div>
              </div>
              <div class="progress-meta">
                <span id="progressFile">Uploading...</span>
                <!-- Deliberately not a live region: role="progressbar" already
                     exposes the value programmatically, and mirroring it here
                     announced this span ~1.3x/second for the whole transfer.
                     The byte/ETA detail now rides on aria-valuetext instead. -->
                <span id="progressPercent">0%</span>
                <button type="button" class="btn-sm" id="btnCancelUpload" data-action="cancel-upload" style="display: none;">✕ Cancel</button>
              </div>
            </div>
            <div class="status-banner" id="dropStatus" role="status" aria-live="polite"></div>
          </div>

          <div class="card">
            <h2 class="card-title">📝 Quick Text & Snippet Sharing</h2>
            <label class="sr-only" for="textPayload">Text snippet to send</label>
            <textarea id="textPayload" placeholder="Paste code snippet, auth tokens, commands, or notes to send immediately to the peer..."></textarea>
            <div style="display: flex; justify-content: flex-end; margin-top: 0.75rem;">
              <button class="btn-primary" id="btnSendText" data-action="send-text">Send to Peer</button>
            </div>
          </div>
        </div>

        <!-- Right Pane: Received Items & History -->
        <div class="card" style="display: flex; flex-direction: column;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
            <h2 class="card-title" style="margin-bottom: 0;">📥 Received Items</h2>
            <button class="btn-sm" data-action="load-recent">🔄 Refresh</button>
          </div>
          <div id="receivedDropsList" style="flex: 1; overflow-y: auto; max-height: 580px; display: flex; flex-direction: column; gap: 0.75rem;">
            <p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.<br>Session items — cleared when the Hub restarts. Sent transfers live under Transfers.</p>
          </div>
          <p id="inboxStatus" class="sr-only" role="status" aria-live="polite"></p>
        </div>
      </div>
    </div>

    <!-- TAB 2: OAUTH RELAY -->
    <div id="tab-relay" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-relay">
      <div class="bookmarklet-box">
        <a class="bookmarklet-btn" href="{{BOOKMARKLET_HREF}}">⚡ Tantu</a>
        <div class="hint-text">Drag this button to your browser Bookmarks Bar. When logging in on any OAuth tab, click it, confirm in the popup, and the authorization is relayed back to your dev terminal. The saved bookmark never expires.</div>
      </div>

      <div class="card">
        <h2 class="card-title">⚡ Manual OAuth URL Forwarder</h2>
        <form data-action="relay-oauth" class="form-group">
          <input type="text" id="oauthUrlInput" aria-label="OAuth authorization URL" placeholder="Paste OAuth URL (https://accounts.google.com/o/oauth2/...)" required>
          <button type="submit" class="btn-primary" id="btnRelay">Relay to Peer</button>
        </form>
        <div class="status-banner" id="relayStatus" role="status" aria-live="polite"></div>
      </div>

      <div class="card">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
          <h2 class="card-title" style="margin-bottom: 0;">🔐 Recent Authorizations</h2>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="load-authorizations">🔄 Refresh</button>
            <button class="btn-sm btn-destructive" data-action="clear-authorizations">🗑️ Clear</button>
          </div>
        </div>
        <p class="hint-text" style="margin-top: 0; margin-bottom: 0.75rem;">Origin hosts only — URLs, codes, and tokens are never stored. Kept 30 days (last 50).</p>
        <div id="authorizationsList" style="display: flex; flex-direction: column; gap: 0.75rem;">
          <p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No authorizations yet.<br>Relay a login above to record its outcome here.</p>
        </div>
      </div>
    </div>

    <!-- TAB 3: PEERS & NETWORK -->
    <div id="tab-peers" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-peers">
      <!-- Discovered Nearby Hubs (Auto-detected via mDNS/LAN Beacon) -->
      <div id="discoveredHubsCard" class="card" style="display: none; margin-bottom: 1.5rem; border: 1px solid var(--accent); background: rgba(59, 130, 246, 0.05);">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
          <h2 class="card-title" style="margin-bottom: 0; color: var(--accent);">⚡ Discovered Nearby Hubs</h2>
          <span style="font-size: 0.75rem; color: var(--text-muted); background: var(--surface-hover); padding: 0.2rem 0.5rem; border-radius: 4px;">Zero-Typing Discovery</span>
        </div>
        <div id="discoveredHubsList" style="display: flex; flex-direction: column; gap: 0.5rem;"></div>
      </div>

      <div class="grid-2">
        <div class="card">
          <h2 class="card-title">💻 Local Hub Identity</h2>
          <div class="stat-row">
            <span class="stat-label">Transport</span>
            <span class="stat-val" id="idTransport">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">SAS Verification Code</span>
            <span class="stat-val mono-accent" id="idSAS">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">P2P Listen Socket</span>
            <span class="stat-val" id="idP2P">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">Web / IPC Socket</span>
            <span class="stat-val" id="idWeb">-</span>
          </div>
          <div class="stat-row">
            <span class="stat-label">TLS Fingerprint</span>
            <span class="stat-val" id="idFP" style="font-size: 0.75rem;">-</span>
          </div>
        </div>

        <div class="card">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
            <h2 class="card-title" style="margin-bottom: 0;">👥 Trusted Peers</h2>
            <button class="btn-sm" data-action="open-pair-modal">+ Pair New Device</button>
          </div>
          <div id="peersList">
            <p style="color: var(--text-muted); font-size: 0.9rem;">Loading peer status...</p>
          </div>
        </div>
      </div>

      <!-- PAIRING MODAL -->
      <div id="pairModal" class="modal-overlay" role="dialog" aria-modal="true" aria-labelledby="pairModalTitle" style="display:none; position:fixed; inset:0; backdrop-filter:blur(4px); z-index:9999; align-items:center; justify-content:center;">
        <div class="modal-box" style="width:90%; max-width:500px; padding:1.5rem; box-shadow:var(--shadow-lg); border-radius:16px;">
          <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:1rem;">
            <h3 id="pairModalTitle" class="pair-title">🔗 Pair New Device</h3>
            <button data-action="close-pair-modal" aria-label="Close pairing dialog" class="pair-close">&times;</button>
          </div>
          
          <p class="pair-text">
            Pairing establishes mutual zero-trust encryption (TLS + SAS authentication) between this machine and another tantu instance over in-band port 9877.
          </p>

          <div class="pair-sas-box">
            <div class="pair-sas-label">Local SAS Verification Code</div>
            <div id="pairModalSAS" class="pair-sas-code">---</div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label class="pair-opt-label">Option 1: Discover and pair from the remote device</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <code id="pairCmdText" class="pair-cmd">tantu pair --peer=&lt;THIS_IP&gt;:9877</code>
              <button class="btn-sm" id="btnCopyPairCmd" data-action="copy-pair-command">📋 Copy</button>
            </div>
          </div>

          <div style="margin-bottom:1.25rem;">
            <label for="pairRemoteAddrInput" class="pair-opt-label">Option 2: Connect to Remote Peer Address</label>
            <div style="display:flex; gap:0.5rem; align-items:center;">
              <input type="text" id="pairRemoteAddrInput" placeholder="192.168.1.50:9877" style="flex:1; font-size:0.85rem; padding:0.5rem 0.75rem;">
              <button class="btn-primary" id="btnPairConnect" data-action="pair-connect" style="white-space:nowrap; padding:0.5rem 1rem;">Pair Device</button>
            </div>
            <div id="pairStatusMsg" role="status" aria-live="polite" style="font-size:0.8rem; margin-top:0.5rem; min-height:1.2rem;"></div>
          </div>

          <div style="display:flex; justify-content:flex-end;">
            <button class="btn-sm" data-action="close-pair-modal">Close</button>
          </div>
        </div>
      </div>
    </div>

<!-- TAB: TRANSFERS (sender truth, durable) -->
    <div id="tab-transfers" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-transfers">
      <div class="card">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem;">
          <h2 class="card-title" style="margin-bottom: 0;">🔄 Transfers</h2>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="load-transfers">🔄 Refresh</button>
            <button class="btn-sm btn-destructive" data-action="clear-transfers">🗑️ Clear</button>
          </div>
        </div>
        <p class="hint-text" style="margin-top: 0; margin-bottom: 0.75rem;">Sender-side truth, kept 30 days (last 50). Received items stay under QuickDrop.</p>
        <div id="transfersList" style="display: flex; flex-direction: column; gap: 0.75rem;">
          <p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No transfers yet.<br>Send text, an image, or a file from QuickDrop.</p>
        </div>
      </div>
    </div>

    <!-- TAB 4: LIVE LOGS -->
    <div id="tab-logs" class="tab-pane" role="tabpanel" aria-labelledby="tab-btn-logs">
      <div class="card">
        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 0.75rem; margin-bottom: 1rem;">
          <h2 class="card-title" style="margin-bottom: 0;">📋 Live Diagnostics & Activity Feed</h2>
          <div style="display: flex; gap: 0.5rem;">
            <button class="btn-sm" data-action="export-logs">📥 Export Logs</button>
            <button class="btn-sm btn-destructive" data-action="clear-logs">🗑️ Clear feed</button>
          </div>
        </div>

        <!-- Filter Pills & Search Bar -->
        <div style="display: flex; flex-direction: column; gap: 0.75rem; margin-bottom: 1rem;">
          <div style="display: flex; gap: 0.6rem; flex-wrap: wrap; align-items: center;" class="log-filters">
            <span style="font-size: 0.8rem; color: var(--text-muted); margin-right: 0.25rem;">Filter:</span>
            <button class="pill active" data-filter="ALL" aria-pressed="true">All</button>
            <button class="pill" data-filter="OAUTH" aria-pressed="false">🔵 OAuth</button>
            <button class="pill" data-filter="DROP" aria-pressed="false">🟢 QuickDrop</button>
            <button class="pill" data-filter="PEER" aria-pressed="false">🟣 Peer</button>
            <button class="pill" data-filter="NET" aria-pressed="false">🟡 Network</button>
            <button class="pill" data-filter="DEBUG" aria-pressed="false">🐛 Debug / Verbose</button>
            <button class="pill" data-filter="ERROR" aria-pressed="false">🔴 Errors</button>
          </div>

          <div style="position: relative;">
            <input type="text" id="logSearchInput" data-action="filter-logs" aria-label="Search live logs" placeholder="🔍 Search live logs by text, URL, host, or request ID..." style="padding-left: 1rem; font-size: 0.85rem;">
          </div>
        </div>

        <!-- role=log gives the region a name and makes it a findable landmark;
             aria-live=off is deliberate: 500 append-only diagnostic lines are
             not status messages and announcing each one would be unusable.
             tabindex=0 makes the scrolled region keyboard-reachable. -->
        <div class="log-console" id="logConsole" tabindex="0" role="log" aria-live="off" aria-label="Live diagnostic log">
          <div class="log-entry" data-domain="NET" data-level="INFO"><span class="log-time" style="color:#8b98ad; margin-right:0.5rem;">--:--:--</span><span class="tag tag-net">NET</span> Hub initialized and listening.</div>
        </div>
      </div>
    </div>
  </main>

  <script nonce="{{NONCE}}">
    // The server tells the page whether the HttpOnly session cookie is already
    // present. This lets an unauthenticated/stale bookmark render a clear
    // recovery state without firing a burst of doomed 401 API requests.
    const pageSessionReady = {{SESSION_READY}};
    async function bootstrapDashboard() {
      const params = new URLSearchParams(location.hash.slice(1));
      const token = params.get('tantu_bootstrap');
      if (!token) return pageSessionReady;
      try {
        const response = await fetch('/api/session', {
          method: 'POST',
          headers: { 'X-Tantu-Dashboard-Bootstrap': token },
          credentials: 'same-origin'
        });
        if (!response.ok) return false;
        history.replaceState(null, '', location.pathname + location.search);
        // Reload once so the server can render a relay bookmarklet only after
        // the HttpOnly session cookie has been established.
        window.location.reload();
        return true;
      } catch (_) {
        return false;
      }
    }
    let dashboardReady = bootstrapDashboard();
    function apiFetch(path, options) {
      options = options || {};
      return dashboardReady.then((ready) => {
        if (!ready) {
          throw new Error('Dashboard session not established. Reopen it from the Hub terminal.');
        }
        const headers = new Headers(options.headers || {});
        const requestOptions = Object.assign({}, options, {
          headers: headers,
          credentials: options.credentials || 'same-origin'
        });
        return fetch(path, requestOptions);
      });
    }

    // CSP-safe event delegation: all user actions are bound from JavaScript
    // rather than inline HTML attributes. Values supplied by peers are read
    // from data attributes or the short-lived receivedActionValues map.
    document.addEventListener('click', function(event) {
      const target = event.target && event.target.closest ? event.target.closest('[data-action], [data-filter], [data-tab]') : null;
      if (!target) return;
      if (target.hasAttribute('data-tab')) {
        switchTab(target.getAttribute('data-tab'), target);
        return;
      }
      if (target.hasAttribute('data-filter')) {
        setLogFilter(target.getAttribute('data-filter'), target);
        return;
      }
      const action = target.getAttribute('data-action');
      switch (action) {
        case 'choose-file':
          document.getElementById('fileInput').click();
          break;
        case 'open-folder':
          openDownloadFolder();
          break;
        case 'change-download-dir':
          promptChangeDownloadDir();
          break;
        case 'cancel-upload':
          cancelUpload();
          break;
        case 'confirm-send':
          confirmPendingSend();
          break;
        case 'cancel-preview':
          cancelPendingSend();
          break;
        case 'load-transfers':
          loadTransfers();
          break;
        case 'load-authorizations':
          loadAuthorizations();
          break;
        case 'clear-authorizations':
          clearAuthorizations();
          break;
        case 'clear-transfers':
          clearTransfers();
          break;
        case 'send-text':
          sendTextDrop();
          break;
        case 'load-recent':
          loadRecentDrops();
          break;
        case 'open-pair-modal':
          openPairModal();
          break;
        case 'close-pair-modal':
          closePairModal();
          break;
        case 'copy-pair-command':
          copyPairCmd();
          break;
        case 'pair-connect':
          initiateWebPairing();
          break;
        case 'export-logs':
          exportLogs();
          break;
        case 'clear-logs':
          // aria-pressed style destructive affordance is visual only; the
          // confirmation lives in clearLogs() so the rule cannot be bypassed
          // by invoking the handler directly.
          clearLogs();
          break;
        case 'select-peer':
          selectDropPeer(target.getAttribute('data-value') || '');
          break;
        case 'decide-pairing':
          decidePairing(target.getAttribute('data-value') || '', target.getAttribute('data-accept') === 'true');
          break;
        case 'prefill-pair':
          prefillPairModal(target.getAttribute('data-address') || '', target.getAttribute('data-sas') || '');
          break;
        case 'set-default-peer':
          setDefaultPeer(target.getAttribute('data-value') || '');
          break;
        case 'edit-alias':
          promptEditAlias(target.getAttribute('data-value') || '', target.getAttribute('data-alias') || '');
          break;
        case 'unpair-peer':
          confirmUnpair(target.getAttribute('data-value') || '', target.getAttribute('data-name') || '');
          break;
        case 'copy-snippet':
          copySnippet(receivedActionValues[target.getAttribute('data-value')] || '', target);
          break;
        case 'open-url':
          openURL(receivedActionValues[target.getAttribute('data-value')] || '');
          break;
      }
    });
    document.addEventListener('submit', function(event) {
      const form = event.target;
      if (form && form.getAttribute && form.getAttribute('data-action') === 'relay-oauth') {
        relayOAuth(event);
      }
    });
    document.addEventListener('input', function(event) {
      const input = event.target;
      if (input && input.getAttribute && input.getAttribute('data-action') === 'filter-logs') {
        applyLogFilters();
      }
    });
    document.addEventListener('change', function(event) {
      const select = event.target;
      if (select && select.getAttribute && select.getAttribute('data-action') === 'active-peer') {
        switchActivePeer(select.value);
      }
    });
    document.addEventListener('keydown', function(event) {
      // Arrow/Home/End inside the tablist move between tabs with automatic
      // activation, so a screen-reader or keyboard user is not forced to Tab
      // through five separate stops to reach Live Logs.
      const tab = event.target && event.target.closest ? event.target.closest('.tab-btn') : null;
      if (tab && (event.key === 'ArrowRight' || event.key === 'ArrowLeft' || event.key === 'Home' || event.key === 'End')) {
        event.preventDefault();
        const tabs = Array.prototype.slice.call(document.querySelectorAll('.tab-btn'));
        let next = tabs.indexOf(tab);
        if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = tabs.length - 1;
        else next = (next + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
        const target = tabs[next];
        if (target) {
          switchTab(target.getAttribute('data-tab'), target);
          target.focus();
        }
        return;
      }
      const modal = document.getElementById('pairModal');
      if (event.key === 'Escape' && modal && modal.style.display !== 'none') {
        event.preventDefault();
        closePairModal();
        return;
      }
      if (modal && modal.style.display !== 'none' && event.key === 'Tab') {
        // Buttons and inputs are the whole dialog today; the second query
        // keeps the trap correct if a link or custom control is ever added,
        // so focus can never leak out of an open modal.
        const focusables = Array.prototype.slice.call(modal.querySelectorAll('button, input'))
          .concat(Array.prototype.slice.call(modal.querySelectorAll('a[href], select, textarea, [tabindex]')));
        if (focusables.length > 0) {
          const first = focusables[0];
          const last = focusables[focusables.length - 1];
          if (event.shiftKey && document.activeElement === first) {
            event.preventDefault();
            last.focus();
          } else if (!event.shiftKey && document.activeElement === last) {
            event.preventDefault();
            first.focus();
          }
        }
        return;
      }
      const zone = event.target && event.target.closest ? event.target.closest('#dropZone') : null;
      if (zone && (event.key === 'Enter' || event.key === ' ')) {
        event.preventDefault();
        document.getElementById('fileInput').click();
      }
    });
    document.addEventListener('error', function(event) {
      const image = event.target;
      if (image && image.matches && image.matches('img.received-item-preview')) {
        image.remove();
      }
    }, true);
    document.addEventListener('load', function(event) {
      const image = event.target;
      if (image && image.matches && (image.matches('img.received-item-preview') || image.id === 'filePreviewThumb')) {
        image.classList.add('loaded');
      }
    }, true);

    function switchTab(tabId, sourceButton) {
      document.querySelectorAll('.tab-btn').forEach(b => {
        b.classList.remove('active');
        b.setAttribute('aria-selected', 'false');
        // Roving tabindex: a tablist is a single tab stop, so only the
        // selected tab stays in the Tab order and arrow keys move within it.
        b.setAttribute('tabindex', '-1');
      });
      document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
      // Click path passes the source button explicitly; programmatic callers
      // (such as paste-to-upload) may omit it and are matched by data-tab.
      const btn = sourceButton || Array.prototype.find.call(document.querySelectorAll('.tab-btn'), b => b.getAttribute('data-tab') === tabId);
      if (btn) {
        btn.classList.add('active');
        btn.setAttribute('aria-selected', 'true');
        btn.setAttribute('tabindex', '0');
      }
      const pane = document.getElementById(tabId);
      if (pane) pane.classList.add('active');
    }

    let currentFilter = 'ALL';

    function addLog(domain, msg, level, meta) {
      domain = (domain || 'SYS').toUpperCase();
      level = (level || 'INFO').toUpperCase();
      const console = document.getElementById('logConsole');
      const row = document.createElement('div');
      row.className = 'log-entry';
      row.setAttribute('data-domain', domain);
      row.setAttribute('data-level', level);

      const timeStr = new Date().toLocaleTimeString();
      let tagClass = 'tag-net';
      if (domain === 'OAUTH') tagClass = 'tag-oauth';
      else if (domain === 'DROP') tagClass = 'tag-drop';
      else if (domain === 'PEER') tagClass = 'tag-peer';
      else if (domain === 'SYS') tagClass = 'tag-sys';
      if (level === 'ERROR') tagClass = 'tag-error';
      else if (level === 'DEBUG') tagClass = 'tag-debug';

      let metaHTML = '';
      if (meta && Object.keys(meta).length > 0) {
        metaHTML = '<details style="margin-top:0.25rem; font-size:0.75rem; color:#94a3b8;"><summary style="cursor:pointer; color:#60a5fa;">inspect metadata</summary><pre style="background:rgba(0,0,0,0.3); padding:0.4rem; border-radius:4px; margin-top:0.25rem; overflow-x:auto;">' + escapeHTML(JSON.stringify(meta, null, 2)) + '</pre></details>';
      }

      row.innerHTML = '<span class="log-time" style="margin-right:0.5rem;">' + timeStr + '</span><span class="tag ' + tagClass + '">' + escapeHTML(domain) + '</span> ' + escapeHTML(msg) + metaHTML;
      console.appendChild(row);

      if (console.children.length > 500) {
        console.removeChild(console.firstChild);
      }

      filterRow(row);
      console.scrollTop = console.scrollHeight;
    }

    function setLogFilter(filter, btn) {
      currentFilter = filter;
      document.querySelectorAll('.pill[data-filter]').forEach(p => {
        p.classList.remove('active');
        p.setAttribute('aria-pressed', 'false');
      });
      if (btn) {
        btn.classList.add('active');
        btn.setAttribute('aria-pressed', 'true');
      }
      applyLogFilters();
    }

    function applyLogFilters() {
      const search = (document.getElementById('logSearchInput').value || '').toLowerCase();
      const rows = document.querySelectorAll('#logConsole .log-entry');
      rows.forEach(row => {
        const rowDomain = row.getAttribute('data-domain') || '';
        const rowLevel = row.getAttribute('data-level') || '';
        const rowText = row.innerText.toLowerCase();

        let domainMatch = true;
        if (currentFilter === 'DEBUG') {
          domainMatch = (rowLevel === 'DEBUG');
        } else if (currentFilter === 'ERROR') {
          domainMatch = (rowLevel === 'ERROR');
        } else if (currentFilter !== 'ALL') {
          domainMatch = (rowDomain === currentFilter);
        }

        const searchMatch = !search || rowText.includes(search);
        row.style.display = (domainMatch && searchMatch) ? '' : 'none';
      });
    }

    function filterRow(row) {
      const search = (document.getElementById('logSearchInput').value || '').toLowerCase();
      const rowDomain = row.getAttribute('data-domain') || '';
      const rowLevel = row.getAttribute('data-level') || '';
      const rowText = row.innerText.toLowerCase();

      let domainMatch = true;
      if (currentFilter === 'DEBUG') {
        domainMatch = (rowLevel === 'DEBUG');
      } else if (currentFilter === 'ERROR') {
        domainMatch = (rowLevel === 'ERROR');
      } else if (currentFilter !== 'ALL') {
        domainMatch = (rowDomain === currentFilter);
      }

      const searchMatch = !search || rowText.includes(search);
      row.style.display = (domainMatch && searchMatch) ? '' : 'none';
    }

    async function exportLogs() {
      try {
        const res = await apiFetch('/api/logs');
        if (!res.ok) {
          alert('Failed to fetch logs: ' + res.statusText);
          return;
        }
        const data = await res.json();
        const jsonStr = JSON.stringify(data, null, 2);
        const blob = new Blob([jsonStr], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'tantu-logs.json';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } catch (err) {
        alert('Error exporting logs: ' + err.message);
      }
    }

    async function loadInitialLogs() {
      try {
        const res = await apiFetch('/api/logs');
        if (!res.ok) return;
        const events = await res.json();
        if (Array.isArray(events) && events.length > 0) {
          document.getElementById('logConsole').innerHTML = '';
          events.forEach(ev => {
            const domain = ev.domain || ev.tag || 'SYS';
            addLog(domain, ev.message, ev.level, ev.metadata);
          });
        }
      } catch (_) {}
    }

    // Clearing the visible console was unconfirmed and unlabelled, while the
    // two sibling Clear buttons (transfers, authorizations) both explain what
    // is removed. Same rule applies here: say what this does and does not do.
    function clearLogs() {
      if (!confirm('Clear the visible log feed?\n\n' +
        'This only empties what is shown in this tab. The Hub keeps its own log, and transfer and authorization history are untouched. ' +
        'Use Export Logs first if you want a copy.')) {
        return;
      }
      document.getElementById('logConsole').innerHTML = '';
    }

    let selectedPeerTarget = '';
    // Short-lived indirection for user-controlled text/URLs keeps large or
    // quote-heavy values out of HTML attributes and inline event handlers.
    let receivedActionValues = Object.create(null);
    let receivedActionSequence = 0;
    function rememberReceivedValue(value) {
      const id = 'received-' + (++receivedActionSequence);
      receivedActionValues[id] = String(value == null ? '' : value);
      return id;
    }
    // Signature of the last rendered peer DOM. updateStatus polls every 3s;
    // rewriting the header <select>, pills, and peer list on every poll
    // steals focus/selection and breaks keyboard interaction, so the peer
    // DOM is only re-rendered when this signature changes (and never while
    // the header select holds focus — the render is deferred to a later poll).
    let lastPeerSignature = '';

    async function updateStatus() {
      try {
        const res = await apiFetch('/api/status');
        if (!res.ok) return;
        const data = await res.json();
        
        document.getElementById('idTransport').innerText = data.transport || 'loopback';
        document.getElementById('idP2P').innerText = data.p2p_addr || '127.0.0.1:9877';
        document.getElementById('idWeb').innerText = data.web_addr || '127.0.0.1:9876';
        if (data.identity) {
          document.getElementById('idSAS').innerText = data.identity.sas || '-';
          document.getElementById('idFP').innerText = data.identity.fingerprint ? data.identity.fingerprint.slice(0, 24) + '...' : '-';
        }

        const peers = data.peers || [];
        lastPeers = peers;
        lastTransport = data.transport || 'loopback';
        // "via mTLS" was asserted unconditionally, but mTLS is only the LAN
        // transport. Name the transport actually in use instead.
        const transportNote = document.getElementById('dropZoneTransportNote');
        if (transportNote) {
          transportNote.textContent = (lastTransport === 'loopback')
            ? 'Delivered to this Hub over the local loopback transport • for testing only'
            : 'Direct encrypted ' + lastTransport + ' streaming • no intermediate server';
        }
        const peersList = document.getElementById('peersList');
        const dropSelector = document.getElementById('dropPeerSelector');
        const dropPills = document.getElementById('dropPeerPills');
        const peerDot = document.getElementById('peerDot');
        const peerLabel = document.getElementById('peerLabel');

        // Normalize selection first so the render signature below is stable.
        if (peers.length === 0) {
          selectedPeerTarget = '';
        } else if (peers.length === 1) {
          selectedPeerTarget = peers[0].fingerprint;
        } else {
          const activePeer = peers.find(p => p.active) || peers[0];
          if (!selectedPeerTarget || !peers.some(p => p.fingerprint === selectedPeerTarget)) {
            selectedPeerTarget = activePeer.fingerprint;
          }
        }

        const peerSig = JSON.stringify(peers.map(p => [p.fingerprint, p.name, p.alias, p.active, p.is_default, p.address])) + '|' + (selectedPeerTarget || '');
        const headerFocused = document.activeElement && document.activeElement.id === 'headerPeerSelect';
        // Skip re-render while the user interacts with the header select;
        // lastPeerSignature stays stale so the deferred update lands on a
        // later poll after blur.
        if (peerSig !== lastPeerSignature && !headerFocused) {
          lastPeerSignature = peerSig;
        if (peers.length === 0) {
          peerLabel.innerText = 'No trusted peers yet';
          peerDot.className = 'status-dot offline';
          peersList.innerHTML = '<p style="color: var(--text-muted); font-size: 0.9rem;">No paired LAN peers yet. Run <code>tantu pair</code> to connect another machine.</p>';
          if (dropSelector) dropSelector.style.display = 'none';
        } else if (peers.length === 1) {
          const p = peers[0];
          peerLabel.innerText = (p.name || 'Peer') + ' · Trusted';
          peerDot.className = 'status-dot';
          if (dropSelector) dropSelector.style.display = 'none';
          renderPeersList(peers);
        } else {
          // Multiple peers
          peerDot.className = 'status-dot';
          const activePeer = peers.find(p => p.active) || peers[0];

          // Header dropdown
          const optionsHTML = peers.map(p => {
            const sel = (p.fingerprint === activePeer.fingerprint) ? 'selected' : '';
            const defBadge = p.is_default ? ' (Default)' : '';
            return '<option value="' + escapeHTML(p.fingerprint) + '" ' + sel + ' style="background:var(--card-bg); color:var(--text);">' + escapeHTML(p.name) + defBadge + '</option>';
          }).join('');
          peerLabel.innerHTML = '<label class="sr-only" for="headerPeerSelect">Active peer</label><select id="headerPeerSelect" data-action="active-peer" aria-label="Active peer" style="background:transparent; color:var(--text); border:none; outline:none; font-size:0.85rem; font-weight:600; cursor:pointer;">' + optionsHTML + '</select>';

          // Tab 1 destination pills
          if (dropSelector && dropPills) {
            dropSelector.style.display = 'flex';
            dropPills.innerHTML = peers.map(p => {
              const isSelected = (p.fingerprint === selectedPeerTarget);
              const pillClass = isSelected ? 'pill active' : 'pill';
              const defTag = p.is_default ? ' ⭐️' : '';
              return '<button type="button" class="' + pillClass + '" data-action="select-peer" data-value="' + escapeHTML(p.fingerprint) + '" aria-pressed="' + (isSelected ? 'true' : 'false') + '">' + escapeHTML(p.name) + defTag + '</button>';
            }).join('');
          }

          renderPeersList(peers);
        }
        }
      } catch (err) {
        document.getElementById('peerLabel').innerText = 'Disconnected';
        document.getElementById('peerDot').className = 'status-dot offline';
      }

      updateDestinationSummary();
      updateNextAction();

      // Query discovered LAN peers
      try {
        const discRes = await apiFetch('/api/discovery/peers');
        if (discRes.ok) {
          const discPeers = await discRes.json();
          renderDiscoveredHubs(discPeers);
        }
      } catch (err) {
        // Discovery endpoint non-critical
      }

      // Check for incoming pairing approvals
      await loadPendingPairings();
    }

    // Signature of the last rendered pending-pairing set. loadPendingPairings
    // runs on every 3s status poll; rebuilding the banner unconditionally threw
    // keyboard focus onto <body> every three seconds, which made approving an
    // inbound request - the one action that grants permanent mutual trust -
    // impossible to complete with a keyboard or screen reader.
    let lastPendingPairingsSignature = '';
    let lastPendingPairingName = '';
    async function loadPendingPairings() {
      try {
        const res = await apiFetch('/api/pair/pending');
        if (!res.ok) return;
        const list = await res.json();
        const banner = document.getElementById('pendingPairingsBanner');
        const container = document.getElementById('pendingPairingsList');
        if (!banner || !container) return;
        if (!list || list.length === 0) {
          lastPendingPairingsSignature = '';
          if (banner.style.display === 'none' && container.innerHTML === '') return;
          banner.style.display = 'none';
          container.innerHTML = '';
          return;
        }
        const signature = JSON.stringify((list || []).map(function(item) {
          return [item.id, item.peer_name, item.remote_addr, item.peer_sas];
        }));
        if (signature === lastPendingPairingsSignature) return;
        lastPendingPairingsSignature = signature;
        // Only the first request is nameable in a single confirm() dialog; a
        // queue of several still requires the user to read the banner.
        lastPendingPairingName = (list[0] && list[0].peer_name) ? String(list[0].peer_name) : '';
        banner.style.display = 'block';
        container.innerHTML = list.map(function(item) {
          var name = escapeHTML(item.peer_name || 'Nearby Device');
          var addr = escapeHTML(item.remote_addr);
          var sas = escapeHTML(item.peer_sas);
          var id = escapeHTML(item.id);
          return '<div class="pairing-row">' +
            '<div>' +
              '<div class="pairing-row-name">' + name + ' <span class="pairing-row-addr">(' + addr + ')</span></div>' +
              '<div class="pairing-row-meta">Compare this code with the code shown on the remote screen: <strong class="pairing-sas">' + sas + '</strong></div>' +
            '</div>' +
            '<div class="pairing-actions">' +
              '<button type="button" class="btn-approve" data-action="decide-pairing" data-value="' + escapeHTML(id) + '" data-accept="true" aria-label="Approve pairing request from ' + name + '">✓ Approve</button>' +
              '<button type="button" class="btn-reject" data-action="decide-pairing" data-value="' + escapeHTML(id) + '" data-accept="false" aria-label="Reject pairing request from ' + name + '">✕ Reject</button>' +
            '</div>' +
          '</div>';
        }).join('');
      } catch (err) {
        // Pending check non-critical
      }
    }

    // Approving an inbound request grants permanent mutual trust on a single
    // click, which is inconsistent with confirming an unpair. The name and
    // SAS are named back so the user confirms the right request.
    function approvePairingConsequenceText(name) {
      return 'Approve pairing with ' + (name || 'this device') + '?\n\n' +
        'This saves its identity as trusted. You can send files, text and OAuth authorizations to it, and it can do the same to you, without asking again.\n\n' +
        'Only approve if the SAS code matches the one shown on that device. If it does not, reject it instead.';
    }

    async function decidePairing(id, accept) {
      if (accept && !confirm(approvePairingConsequenceText(lastPendingPairingName))) {
        return;
      }
      try {
        const res = await apiFetch('/api/pair/decision', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, accept })
        });
        if (res.ok) {
          await loadPendingPairings();
          await updateStatus();
        }
      } catch (err) {
        console.error('Pairing decision failed', err);
      }
    }

    function renderDiscoveredHubs(discovered) {
      const card = document.getElementById('discoveredHubsCard');
      const list = document.getElementById('discoveredHubsList');
      if (!card || !list) return;

      const unpaired = (discovered || []).filter(d => !d.is_paired);
      if (unpaired.length === 0) {
        card.style.display = 'none';
        return;
      }

      card.style.display = 'block';
      list.innerHTML = unpaired.map(function(d) {
        return '<div style="display: flex; justify-content: space-between; align-items: center; background: var(--surface); padding: 0.6rem 0.85rem; border-radius: 6px; border: 1px solid var(--border);">' +
          '<div>' +
            '<div style="font-weight: 600; font-size: 0.9rem; color: var(--text);">💻 ' + escapeHTML(d.name) + '</div>' +
            '<div style="font-size: 0.75rem; color: var(--text-muted); font-family: monospace;">' + escapeHTML(d.address) + ' · unverified SAS: <span style="color: var(--accent); font-weight: 600;">' + escapeHTML(d.sas) + '</span></div>' +
          '</div>' +
          '<button class="btn btn-primary" style="padding: 0.3rem 0.75rem; font-size: 0.8rem;" data-action="prefill-pair" data-address="' + escapeHTML(d.address) + '" data-sas="' + escapeHTML(d.sas) + '">⚡ Pair</button>' +
        '</div>';
      }).join('');
    }

    function prefillPairModal(addr, sas) {
      openPairModal();
      const input = document.getElementById('pairRemoteAddrInput') || document.getElementById('pairPeerAddress');
      if (input) {
        input.value = addr;
        input.focus();
      }
      if (sas) {
        const statusMsg = document.getElementById('pairStatusMsg');
        if (statusMsg) {
          statusMsg.innerHTML = '<span style="color:var(--accent);">Peer SAS: <strong>' + escapeHTML(sas) + '</strong></span>';
        }
      }
    }

    function selectDropPeer(fp) {
      selectedPeerTarget = fp;
      updateStatus();
    }

    async function switchActivePeer(fp) {
      try {
        const res = await apiFetch('/api/peers/active', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ peer: fp })
        });
        if (res.ok) {
          selectedPeerTarget = fp;
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to switch active peer: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error switching active peer: ' + err.message);
      }
    }

    function renderPeersList(peers) {
      const peersList = document.getElementById('peersList');
      peersList.innerHTML = peers.map(function(pr) {
        const defaultBadge = pr.is_default ? '<span class="tag tag-oauth">Default</span>' : '';
        const activeBadge = pr.active ? '<span class="tag tag-drop">Active</span>' : '';
        const defaultBtn = !pr.is_default ? '<button class="btn-sm" data-action="set-default-peer" data-value="' + escapeHTML(pr.fingerprint) + '">⭐️ Set Default</button>' : '';
        const fpShort = pr.fingerprint ? (pr.fingerprint.slice(0, 16) + '...') : '-';

        return '<div style="background: rgba(255,255,255,0.02); border:1px solid var(--border); border-radius:8px; padding:0.85rem 1rem; margin-bottom:0.75rem;">' +
          '<div style="display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:0.5rem;">' +
            '<div style="display:flex; align-items:center; gap:0.5rem;">' +
              '<span style="font-weight:600; color:var(--text); font-size:1rem;">' + escapeHTML(pr.name || 'Unnamed Peer') + '</span>' +
              defaultBadge + activeBadge +
            '</div>' +
            '<div style="display:flex; gap:0.35rem;">' +
              '<button class="btn-sm" data-action="edit-alias" data-value="' + escapeHTML(pr.fingerprint) + '" data-alias="' + escapeHTML(pr.alias || pr.name || '') + '">✏️ Alias</button>' +
              defaultBtn +
              '<button class="btn-sm" style="color:#f87171; border-color:rgba(239,68,68,0.3);" data-action="unpair-peer" data-value="' + escapeHTML(pr.fingerprint) + '" data-name="' + escapeHTML(pr.name || '') + '">🗑️ Unpair</button>' +
            '</div>' +
          '</div>' +
          '<div style="font-size:0.8rem; color:var(--text-muted); margin-top:0.5rem; display:flex; gap:1rem; flex-wrap:wrap;">' +
            '<span>Address: <strong style="color:var(--text);">' + escapeHTML(pr.address) + '</strong></span>' +
            '<span>SAS: <strong style="color:#60a5fa;">' + escapeHTML(pr.sas) + '</strong></span>' +
            '<span>FP: <span style="font-family:monospace; font-size:0.75rem;">' + escapeHTML(fpShort) + '</span></span>' +
          '</div>' +
        '</div>';
      }).join('');
    }

    // Unpairing is a trust change, not a delete, so the confirmation says what
    // it revokes, what it does not touch, and whether it is reversible. The
    // old copy said only "revoke mutual zero-trust encryption".
    function unpairConsequenceText(name) {
      const label = name || 'this peer';
      return 'Unpair from ' + label + '?\n\n' +
        'This revokes the stored identity: the two machines will no longer accept each other, and a new pairing with fresh SAS verification is required before you can send anything again.\n\n' +
        'Files already received stay on this machine. Nothing is deleted.';
    }

    async function promptEditAlias(fp, currentAlias) {
      const newAlias = prompt('Enter friendly nickname / alias for this peer:', currentAlias || '');
      if (newAlias === null) return;
      try {
        const res = await apiFetch('/api/peers/alias', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp, alias: newAlias.trim() })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to update alias: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error updating alias: ' + err.message);
      }
    }

    async function setDefaultPeer(fp) {
      try {
        const res = await apiFetch('/api/peers/default', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to set default peer: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error setting default peer: ' + err.message);
      }
    }

    async function confirmUnpair(fp, name) {
      if (!confirm(unpairConsequenceText(name))) {
        return;
      }
      try {
        const res = await apiFetch('/api/peers/remove', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ fingerprint: fp })
        });
        if (res.ok) {
          updateStatus();
        } else {
          const d = await res.json();
          alert('Failed to unpair: ' + (d.message || res.statusText));
        }
      } catch (err) {
        alert('Error unpairing: ' + err.message);
      }
    }

    let pairModalReturnFocus = null;
    let pairModalOpener = null;
    function openPairModal() {
      // The dialog nests inside the Peers tab pane, so make its tab visible
      // first: opening it from any other tab would otherwise set display on
      // a node hidden by an ancestor. This keeps the single dialog instance
      // correct for present and future callers alike.
      switchTab('tab-peers');
      const modal = document.getElementById('pairModal');
      // Remember the opener by selector as well as by node: a programmatic
      // open has no focused element to restore to, and a node captured at
      // open time may have been re-rendered away by a status poll.
      pairModalReturnFocus = document.activeElement;
      pairModalOpener = document.querySelector('[data-action="open-pair-modal"]');
      const sas = document.getElementById('idSAS').innerText || '---';
      document.getElementById('pairModalSAS').innerText = sas;
      document.getElementById('pairStatusMsg').innerText = '';
      // Copy a command that actually runs. The dialog used to overwrite the
      // placeholder with a bare "tantu pair", which carries neither an address
      // nor a port, so pasting it on the remote machine did nothing useful.
      // The listening address is already polled into idP2P, so use it.
      const p2p = (document.getElementById('idP2P').innerText || '').trim();
      const pairCmd = document.getElementById('pairCmdText');
      if (pairCmd) {
        pairCmd.textContent = (/:\d+$/.test(p2p) && p2p.indexOf('0.0.0.0') !== 0)
          ? 'tantu pair --peer ' + p2p
          : 'tantu pair --peer <remote-ip>:9877';
      }
      modal.style.display = 'flex';
      const input = document.getElementById('pairRemoteAddrInput');
      if (input) input.focus();
    }

    function closePairModal() {
      const modal = document.getElementById('pairModal');
      if (!modal || modal.style.display === 'none') return;
      modal.style.display = 'none';
      // Never dump a keyboard user on <body>: fall back through the captured
      // node, the opener, then the tab that owns the dialog.
      const candidates = [pairModalReturnFocus, pairModalOpener, document.querySelector('[data-tab="tab-peers"]')];
      for (const el of candidates) {
        // <body> is connected and focusable, so it would silently win and
        // dump the user at the top of the document. It is never a valid
        // restore target: a programmatic open has no meaningful prior focus.
        if (!el || el === document.body || typeof el.focus !== 'function' || !el.isConnected) continue;
        el.focus();
        break;
      }
      pairModalReturnFocus = null;
      pairModalOpener = null;
    }

    async function copyPairCmd() {
      const text = document.getElementById('pairCmdText').innerText;
      const btn = document.getElementById('btnCopyPairCmd');
      try {
        if (navigator.clipboard && window.isSecureContext) {
          await navigator.clipboard.writeText(text);
        } else {
          const ta = document.createElement('textarea');
          ta.value = text;
          document.body.appendChild(ta);
          ta.select();
          document.execCommand('copy');
          document.body.removeChild(ta);
        }
        const oldText = btn.innerText;
        btn.innerText = '✅ Copied!';
        setTimeout(() => { btn.innerText = oldText; }, 2000);
      } catch (e) {
        alert('Failed to copy: ' + e.message);
      }
    }

    async function initiateWebPairing() {
      const input = document.getElementById('pairRemoteAddrInput');
      const addr = (input.value || '').trim();
      const statusMsg = document.getElementById('pairStatusMsg');
      const btn = document.getElementById('btnPairConnect');
      if (!addr) {
        statusMsg.style.color = '#f87171';
        statusMsg.innerText = 'Please enter a valid peer address (e.g. 192.168.1.50:9877).';
        return;
      }
      btn.disabled = true;
      btn.innerText = 'Pairing...';
      statusMsg.style.color = '#38bdf8';
      statusMsg.innerText = 'Connecting to ' + addr + ' for in-band pairing handshake...';

      try {
        const res = await apiFetch('/api/pair/initiate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ peer_addr: addr })
        });
        const data = await res.json();
        if (res.ok && data.accepted === true) {
          statusMsg.style.color = '#4ade80';
          statusMsg.innerText = '✅ Successfully paired with ' + (data.peer_name || addr) + ' (SAS: ' + data.peer_sas + ')';
          input.value = '';
          updateStatus();
        } else if (data.status === 'rejected' || data.accepted === false) {
          statusMsg.style.color = '#f87171';
          statusMsg.innerText = '❌ Pairing was rejected by the remote device.';
        } else {
          statusMsg.style.color = '#f87171';
          statusMsg.innerText = '❌ Pairing failed: ' + (data.error || res.statusText);
        }
      } catch (err) {
        statusMsg.style.color = '#f87171';
        statusMsg.innerText = '❌ Network error: ' + err.message;
      } finally {
        btn.disabled = false;
        btn.innerText = 'Pair Device';
      }
    }

    // Drag and Drop
    const dropZone = document.getElementById('dropZone');
    const fileInput = document.getElementById('fileInput');

    ['dragenter', 'dragover'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.add('dragover'); });
    });
    ['dragleave', 'drop'].forEach(name => {
      dropZone.addEventListener(name, (e) => { e.preventDefault(); dropZone.classList.remove('dragover'); });
    });

    dropZone.addEventListener('drop', (e) => {
      if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
        stageFileForSend(e.dataTransfer.files[0]);
      }
    });
    fileInput.addEventListener('change', () => {
      if (fileInput.files && fileInput.files.length > 0) {
        stageFileForSend(fileInput.files[0]);
      }
    });

    // Clipboard-first uploads: pasting an image (screenshot, copied file)
    // anywhere on the dashboard stages it for preview and confirmation
    // through the authenticated upload path. Text pastes fall through to the
    // focused field (e.g. the snippet textarea). Only file payloads are
    // logged (name + size); no clipboard content ever enters logs or URLs.
    document.addEventListener('paste', (e) => {
      const files = (e.clipboardData && e.clipboardData.files) || [];
      if (files.length === 0) return;
      const image = Array.prototype.find.call(files, f => f.type && f.type.startsWith('image/')) || files[0];
      if (!image) return;
      e.preventDefault();
      const tab = document.getElementById('tab-drop');
      if (tab && !tab.classList.contains('active')) {
        switchTab('tab-drop');
      }
      // Move keyboard/screen-reader context to the upload target after the
      // automatic tab switch. Focusing never opens the file dialog (click
      // only); the outcome is also announced via the aria-live status region.
      stageFileForSend(image);
    });

    // At most one dashboard file upload at a time. A second upload while one
    // is in flight is rejected with a message (rather than queued or
    // silently orphaned) so progress, completion, and cancellation always
    // refer to exactly one transfer.
    let uploadXhr = null;
    let uploadLastLoaded = 0;
    let uploadLastTick = 0;

    function formatDuration(totalSeconds) {
      totalSeconds = Math.max(0, Math.round(totalSeconds));
      if (totalSeconds < 60) return totalSeconds + 's';
      const mins = Math.floor(totalSeconds / 60);
      const secs = totalSeconds % 60;
      return mins + 'm ' + (secs < 10 ? '0' : '') + secs + 's';
    }

    function cancelUpload() {
      if (uploadXhr) {
        uploadXhr.abort();
      }
    }

    // Safe send composer: every file (picker, drag/drop, clipboard paste)
    // stages for preview and explicit confirmation by default. Expert
    // immediate-send is an explicit visible opt-in; it never hides the
    // destination. Only file name and size are logged, never content.
    let pendingFile = null;
    let pendingFileURL = null;
    let pendingFileReturnFocus = null;
    let lastPeers = [];
    let lastTransport = 'loopback';

    function expertImmediateSendEnabled() {
      try {
        const box = document.getElementById('expertImmediateSend');
        if (box) return box.checked;
      } catch (_) {}
      return false;
    }

    function destinationDisplayName() {
      const peers = lastPeers || [];
      if (peers.length === 0) {
        if (lastTransport === 'loopback') return 'this Hub (local-only test)';
        return 'no trusted peer — pair first';
      }
      if (peers.length === 1) {
        return peers[0].name || 'Peer';
      }
      const sel = peers.filter(function(p) { return p.fingerprint === selectedPeerTarget; })[0]
        || peers.filter(function(p) { return p.active; })[0]
        || peers[0];
      return (sel && sel.name) || 'Peer';
    }

    // The send/relay buttons used to say "Send to Peer" regardless of where
    // the payload actually went, which contradicted the destination summary
    // sitting directly above them. Name the resolved destination instead.
    function updateComposerActionLabels() {
      const dest = destinationDisplayName();
      const sendBtn = document.getElementById('btnSendText');
      if (sendBtn) sendBtn.textContent = 'Send text to ' + dest;
      const relayBtn = document.getElementById('btnRelay');
      if (relayBtn) relayBtn.textContent = 'Relay to ' + dest;
    }

    function updateDestinationSummary() {
      const el = document.getElementById('destinationSummary');
      if (!el) return;
      el.textContent = 'Destination: ' + destinationDisplayName();
      updateComposerActionLabels();
      const previewDest = document.getElementById('filePreviewDest');
      if (previewDest && pendingFile) {
        previewDest.textContent = 'Destination: ' + destinationDisplayName();
      }
    }

    // updateNextAction runs on every 3s poll. #nextActionBanner is a live
    // status region, so reassigning its innerHTML unconditionally re-announced
    // the same sentence forever. Only write when the guidance actually changes.
    let lastNextActionHTML = '';
    function updateNextAction() {
      const banner = document.getElementById('nextActionBanner');
      if (!banner) return;
      const peers = lastPeers || [];
      let html = '';
      if (peers.length === 0) {
        if (lastTransport === 'loopback') {
          html = 'Local-only mode: send a test file to this Hub, or run <code>tantu pair</code> to connect another machine.';
        } else {
          html = 'No trusted peers yet. Run <code>tantu pair</code> to connect another machine, then send.';
        }
      } else if (pendingFile) {
        html = 'Review the file preview below, confirm the destination, then Send.';
      } else {
        html = 'Choose text, an image, or a file above. The destination is shown before anything is sent.';
      }
      if (html === lastNextActionHTML) return;
      lastNextActionHTML = html;
      banner.innerHTML = html;
      banner.style.display = 'block';
    }

    // The sticky header plus tab bar is 129px at desktop width and taller on
    // narrow screens once the header wraps. scroll-margin-top is derived from
    // the measured height so a focused control is never parked underneath it.
    function measureChrome() {
      const header = document.querySelector('header');
      const nav = document.querySelector('nav.tabs-nav');
      if (!header || !nav) return;
      const height = header.offsetHeight + nav.offsetHeight;
      if (height > 0) {
        document.documentElement.style.setProperty('--chrome-h', height + 'px');
      }
    }
    window.addEventListener('resize', measureChrome);

    function clearPendingPreviewURL() {
      if (pendingFileURL) {
        try { URL.revokeObjectURL(pendingFileURL); } catch (_) {}
        pendingFileURL = null;
      }
    }

    function stageFileForSend(file) {
      if (!file) return;
      if (uploadXhr) {
        const status = document.getElementById('dropStatus');
        status.style.display = 'block';
        status.className = 'status-banner error';
        status.textContent = 'An upload is already in progress — cancel it before starting another.';
        return;
      }
      if (expertImmediateSendEnabled()) {
        uploadFile(file);
        return;
      }
      pendingFileReturnFocus = document.activeElement;
      pendingFile = file;
      clearPendingPreviewURL();
      const card = document.getElementById('filePreviewCard');
      const title = document.getElementById('filePreviewTitle');
      const meta = document.getElementById('filePreviewMeta');
      const dest = document.getElementById('filePreviewDest');
      const thumb = document.getElementById('filePreviewThumb');
      const type = file.type || 'unknown type';
      title.textContent = 'Ready to send';
      meta.textContent = (file.name || 'pasted image') + ' (' + formatBytes(file.size) + ', ' + type + ')';
      dest.textContent = 'Destination: ' + destinationDisplayName();
      if (thumb) {
        thumb.style.display = 'none';
        thumb.removeAttribute('src');
        if (file.type && file.type.indexOf('image/') === 0 && file.size > 0 && file.size <= 8 * 1024 * 1024) {
          try {
            pendingFileURL = URL.createObjectURL(file);
            thumb.src = pendingFileURL;
            thumb.alt = 'Preview of ' + (file.name || 'pasted image');
            thumb.style.display = 'block';
          } catch (_) {}
        }
      }
      if (card) card.hidden = false;
      addLog('DROP', 'Staged file for preview: ' + (file.name || 'pasted image') + ' (' + file.size + ' bytes)');
      updateNextAction();
      const confirm = document.getElementById('btnConfirmSend');
      if (confirm && typeof confirm.focus === 'function') confirm.focus();
    }

    function cancelPendingSend() {
      pendingFile = null;
      clearPendingPreviewURL();
      const card = document.getElementById('filePreviewCard');
      if (card) card.hidden = true;
      const fileInput = document.getElementById('fileInput');
      if (fileInput) fileInput.value = '';
      updateNextAction();
      if (pendingFileReturnFocus && typeof pendingFileReturnFocus.focus === 'function') {
        try { pendingFileReturnFocus.focus(); } catch (_) {}
      }
      pendingFileReturnFocus = null;
    }

    function confirmPendingSend() {
      if (!pendingFile) return;
      const file = pendingFile;
      pendingFile = null;
      clearPendingPreviewURL();
      const card = document.getElementById('filePreviewCard');
      if (card) card.hidden = true;
      const fileInput = document.getElementById('fileInput');
      if (fileInput) fileInput.value = '';
      updateNextAction();
      uploadFile(file);
    }

    function transferChipClass(state) {
      if (state === 'completed') return 'ok';
      // duplicate_risk is a warning, not a failure: the file may already be
      // saved and a blind retry is what causes the duplicate.
      if (state === 'duplicate_risk') return 'warn';
      if (state === 'terminal_failure' || state === 'failed') return 'err';
      if (state === 'retryable_failure') return 'warn';
      if (state === 'cancelled') return 'neutral';
      return 'info';
    }

    // One vocabulary for operation state, shared by Transfers and
    // Authorizations. Raw taxonomy on a safety-critical state is not a label,
    // it is a leak: "duplicate_risk" says nothing about what the user should
    // do, and rendering it verbatim invited a blind retry.
    const OPERATION_STATE_LABELS = {
      completed: 'File saved on peer',
      completed_with_warning: 'Saved on peer, with a warning',
      duplicate_risk: 'File may already be saved',
      retryable_failure: 'Not delivered',
      terminal_failure: 'Not delivered',
      failed: 'Not delivered',
      cancelled: 'Cancelled by you',
      stale: 'Result unknown',
      unknown: 'Result unknown',
      incomplete: 'Incomplete',
      sending: 'Sending',
      publishing: 'Saving on peer',
      submitted: 'Waiting to send',
      negotiating: 'Connecting to peer',
      waiting_callback: 'Waiting for the browser',
      browser_opened: 'Browser opened',
      callback_received: 'Authorization received',
      verifying: 'Verifying',
      complete: 'Authorization relayed',
    };
    function operationStateLabel(state, isAuthorization) {
      if (!state) return isAuthorization ? 'Result unknown' : 'Result unknown';
      if (OPERATION_STATE_LABELS[state]) return OPERATION_STATE_LABELS[state];
      // Unknown states degrade to something honest instead of leaking the
      // internal identifier into the UI.
      return 'Unrecognised state';
    }

    // Records are kept 30 days, so a bare clock time cannot answer "when".
    // Same-day records read as "today at HH:MM"; older ones carry the date.
    function formatRecordTime(iso) {
      if (!iso) return '';
      const when = new Date(iso);
      if (isNaN(when.getTime())) return '';
      const now = new Date();
      const time = when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
      const sameDay = when.getFullYear() === now.getFullYear() &&
        when.getMonth() === now.getMonth() && when.getDate() === now.getDate();
      if (sameDay) return 'today ' + time;
      const yesterday = new Date(now.getTime() - 86400000);
      const isYesterday = when.getFullYear() === yesterday.getFullYear() &&
        when.getMonth() === yesterday.getMonth() && when.getDate() === yesterday.getDate();
      if (isYesterday) return 'yesterday ' + time;
      return when.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + time;
    }

    function renderTransfers(items) {
      const list = document.getElementById('transfersList');
      if (!list) return;
      if (!items || items.length === 0) {
        list.innerHTML = '<p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No transfers yet.<br>Send text, an image, or a file from QuickDrop.</p>';
        return;
      }
      const reversed = items.slice().reverse();
      list.innerHTML = reversed.map(function(op) {
        const when = op.updated_at || op.created_at;
        const timeStr = formatRecordTime(when);
        const name = op.name || op.kind || 'transfer';
        const sizeStr = formatBytes(op.size || 0);
        const dest = op.destination || 'unknown destination';
        const state = op.state || 'unknown';
        const label = operationStateLabel(state, false);
        let extra = '';
        // verified is only claimed when the receiver acknowledged the digest.
        if (op.verified && state === 'completed') extra += ' · digest verified';
        if (op.duplicate_risk) extra += ' · check the receiving inbox before retrying';
        else if (op.retry_safe && state !== 'completed') extra += ' · safe to retry';
        let recovery = '';
        if (op.next_action && state !== 'completed') {
          recovery = '<div class="record-next">Next: ' + escapeHTML(op.next_action) + '</div>';
        }
        const timePart = timeStr ? ' &bull; ' + escapeHTML(timeStr) : '';
        return '<div class="received-item">' +
          '<div class="received-item-header"><span><strong>' + escapeHTML(name) + '</strong> to ' + escapeHTML(dest) + '</span>' +
          '<span>' + escapeHTML(sizeStr) + timePart + '</span></div>' +
          '<div class="record-state"><span class="status-chip ' + transferChipClass(state) + '">' + escapeHTML(label) + escapeHTML(extra) + '</span></div>' +
          recovery +
        '</div>';
      }).join('');
    }

    function renderAuthorizations(items) {
      const list = document.getElementById('authorizationsList');
      if (!list) return;
      if (!items || items.length === 0) {
        list.innerHTML = '<p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No authorizations yet.<br>Relay a login above to record its outcome here.</p>';
        return;
      }
      const reversed = items.slice().reverse();
      list.innerHTML = reversed.map(function(op) {
        const when = op.updated_at || op.created_at;
        const timeStr = formatRecordTime(when);
        const dest = op.target_peer || 'unknown peer';
        const origin = op.safe_origin || 'unknown origin';
        const state = op.state || 'unknown';
        const label = operationStateLabel(state, true);
        let recovery = '';
        if (op.next_action && state !== 'complete') {
          recovery = '<div class="record-next">Next: ' + escapeHTML(op.next_action) + '</div>';
        }
        const timePart = timeStr ? ' &bull; ' + escapeHTML(timeStr) : '';
        return '<div class="received-item">' +
          '<div class="received-item-header"><span><strong>🔐 ' + escapeHTML(origin) + '</strong> via ' + escapeHTML(dest) + '</span>' +
          '<span>' + escapeHTML(timePart.replace(/^ &bull; /, '')) + '</span></div>' +
          '<div class="record-state"><span class="status-chip ' + transferChipClass(state) + '">' + escapeHTML(label) + '</span></div>' +
          recovery +
        '</div>';
      }).join('');
    }

    async function loadAuthorizations() {
      try {
        const res = await apiFetch('/api/relay/recent');
        if (!res.ok) return;
        const items = await res.json();
        renderAuthorizations(items);
      } catch (_) {}
    }

    async function clearAuthorizations() {
      if (!confirm('Clear authorization history? This removes sender-side metadata only (peers, origins, states). Received files and transfer history are kept. This cannot be undone.')) {
        return;
      }
      try {
        const res = await apiFetch('/api/relay/recent', { method: 'DELETE' });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          addLog('OAUTH', 'Authorization history cleared.');
        } else {
          addLog('ERROR', 'Failed to clear authorizations: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        addLog('ERROR', 'Failed to clear authorizations: ' + err.message);
      }
      loadAuthorizations();
    }

    async function loadTransfers() {
      try {
        const res = await apiFetch('/api/transfers/recent');
        if (!res.ok) return;
        const items = await res.json();
        renderTransfers(items);
      } catch (_) {}
    }

    async function clearTransfers() {
      if (!confirm('Clear transfer history? This removes sender-side metadata only (destinations, states, times). Received files are kept. This cannot be undone.')) {
        return;
      }
      try {
        const res = await apiFetch('/api/transfers/recent', { method: 'DELETE' });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          addLog('DROP', 'Transfer history cleared.');
        } else {
          addLog('ERROR', 'Failed to clear transfers: ' + (data.message || 'Unknown error'));
        }
      } catch (err) {
        addLog('ERROR', 'Failed to clear transfers: ' + err.message);
      }
      loadTransfers();
    }

    async function uploadFile(file) {
      const progress = document.getElementById('uploadProgress');
      const bar = document.getElementById('progressBar');
      const fill = document.getElementById('progressFill');
      const pFile = document.getElementById('progressFile');
      const pPercent = document.getElementById('progressPercent');
      const status = document.getElementById('dropStatus');
      const cancelBtn = document.getElementById('btnCancelUpload');
      const fileInput = document.getElementById('fileInput');

      if (uploadXhr) {
        status.style.display = 'block';
        status.className = 'status-banner error';
        status.textContent = '⚠️ An upload is already in progress — cancel it before starting another.';
        return;
      }
      // apiFetch waits for the bootstrap exchange; XHR must do the same so
      // the session cookie exists before the upload is sent. Avoid opening a
      // doomed request when this page was opened without a Hub session.
      const ready = await dashboardReady;
      if (!ready) {
        status.style.display = 'block';
        status.className = 'status-banner error';
        status.textContent = '⚠️ Dashboard session not established. Reopen it from the Hub terminal.';
        return;
      }

      progress.style.display = 'block';
      cancelBtn.style.display = 'inline-block';
      fill.style.width = '0%';
      bar.setAttribute('aria-valuenow', '0');
      uploadLastLoaded = 0;
      uploadLastTick = Date.now();
      pFile.innerText = file.name + ' (' + formatBytes(file.size) + ')';
      pPercent.innerText = 'Preparing...';
      status.style.display = 'none';

      addLog('DROP', 'Initiating file upload: ' + file.name + ' (' + file.size + ' bytes)');

      const fd = new FormData();
      fd.append('file', file);
      if (selectedPeerTarget) {
        fd.append('peer', selectedPeerTarget);
      }

      const xhr = new XMLHttpRequest();
      uploadXhr = xhr;
      xhr.open('POST', '/api/drop/upload');
      xhr.withCredentials = true;
      xhr.upload.onprogress = (e) => {
        // The bar tracks every event; the text (screen-reader announced)
        // refreshes at most twice per second with speed and ETA.
        const now = Date.now();
        if (e.lengthComputable) {
          const pct = Math.floor((e.loaded / e.total) * 100);
          fill.style.width = pct + '%';
          bar.setAttribute('aria-valuenow', String(pct));
          if (now - uploadLastTick >= 500 || pct >= 100) {
            const elapsed = Math.max((now - uploadLastTick) / 1000, 0.001);
            const speed = Math.max(Math.round((e.loaded - uploadLastLoaded) / elapsed), 0);
            let extra = '';
            if (speed > 0) {
              extra = ' · ' + formatBytes(speed) + '/s';
              const remaining = e.total - e.loaded;
              if (remaining > 0) extra += ' · ETA ' + formatDuration(remaining / speed);
            }
            pPercent.innerText = pct + '% · ' + formatBytes(e.loaded) + ' / ' + formatBytes(e.total) + extra;
            // The visible span is no longer a live region, so carry the same
            // detail on the progressbar itself: a screen reader gets bytes and
            // ETA on demand instead of a spoken counter twice a second.
            bar.setAttribute('aria-valuetext', pct + '%, ' + formatBytes(e.loaded) + ' of ' + formatBytes(e.total) + (extra ? extra.replace(/^ · /, ', ') : ''));
            uploadLastLoaded = e.loaded;
            uploadLastTick = now;
          }
        } else {
          pPercent.innerText = formatBytes(e.loaded) + ' sent...';
        }
      };
      const finishUpload = (ok, message, server) => {
        uploadXhr = null;
        cancelBtn.style.display = 'none';
        if (fileInput) fileInput.value = '';
        status.style.display = 'block';
        if (ok) {
          fill.style.width = '100%';
          bar.setAttribute('aria-valuenow', '100');
          pPercent.innerText = '100% · ' + formatBytes(file.size) + ' / ' + formatBytes(file.size);
          status.className = 'status-banner success';
          const dest = (server && server.destination) || destinationDisplayName();
          const op = (server && server.operation_id) ? ' (ID ' + server.operation_id + ')' : '';
          status.textContent = 'File sent to ' + dest + ' · verified' + op;
          addLog('DROP', 'Completed transfer of ' + file.name + ' to ' + dest);
          setTimeout(() => { progress.style.display = 'none'; fill.style.width = '0%'; }, 2500);
        } else {
          status.className = 'status-banner error';
          status.textContent = message;
          // Keep the progress bar visible on failure so the final state is
          // inspectable; the next upload resets it.
        }
        loadTransfers();
        updateNextAction();
      };
      xhr.onload = () => {
        let message = 'Unknown error';
        let server = null;
        try {
          const data = JSON.parse(xhr.responseText);
          if (xhr.status >= 200 && xhr.status < 300 && data.status === 'success') {
            finishUpload(true, '', data);
            return;
          }
          server = data;
          message = data.plain_message || data.message || ('HTTP ' + xhr.status);
          if (data.next_action) message += ' Next: ' + data.next_action;
          if (data.duplicate_risk) message = 'File may already be saved. ' + message;
          if (data.operation_id) message += ' (ID ' + data.operation_id + ')';
        } catch (_) {
          message = 'HTTP ' + xhr.status;
        }
        if (xhr.status === 503) {
          message += ' (server busy — wait a moment and retry)';
        }
        addLog('ERROR', 'Drop failed: ' + message);
        finishUpload(false, 'Transfer failed: ' + message, server);
      };
      xhr.onerror = () => {
        addLog('ERROR', 'Upload error: connection failed');
        finishUpload(false, '❌ Connection error during upload.');
      };
      xhr.onabort = () => {
        addLog('DROP', 'Upload cancelled: ' + file.name);
        finishUpload(false, '⚠️ Upload cancelled.');
      };
      xhr.send(fd);
    }

    async function sendTextDrop() {
      const textarea = document.getElementById('textPayload');
      const text = textarea.value.trim();
      if (!text) return;

      // Fail fast client-side with the same 10 MiB byte limit the server
      // enforces, instead of uploading megabytes just to be rejected.
      // TextEncoder measures UTF-8 bytes (what the server counts), not
      // UTF-16 code units. Only lengths are logged, never content.
      const textBytes = new TextEncoder().encode(text).length;
      // Client-side mirror of the server text limit (drop.DefaultMaxTextSize =
      // 10 MiB, enforced again server-side): fail fast instead of uploading
      // megabytes the server will reject.
      const maxTextBytes = 10 * 1024 * 1024;
      if (textBytes > maxTextBytes) {
        // Report it on the tab the user clicked from. A silent rejection is
        // indistinguishable from a dead button, and the draft is kept so
        // nothing the user typed is lost to a limit they cannot see.
        const banner = document.getElementById('dropStatus');
        banner.style.display = 'block';
        banner.className = 'status-banner error';
        banner.textContent = 'This snippet is ' + formatBytes(textBytes) + ', over the 10.0 MB limit. Send it as a file instead, or trim it here first. Nothing was sent.';
        addLog('ERROR', 'Text snippet too large (' + formatBytes(textBytes) + ' exceeds the 10.0 MB limit). Send it as a file instead.');
        return;
      }

      const btn = document.getElementById('btnSendText');
      btn.disabled = true;
      const banner = document.getElementById('dropStatus');
      banner.style.display = 'block';
      banner.className = 'status-banner relaying';
      banner.textContent = 'Sending ' + formatBytes(textBytes) + ' to ' + destinationDisplayName() + '…';
      addLog('DROP', 'Sending text snippet (' + text.length + ' chars, ' + formatBytes(textBytes) + ')...');

      const payload = { text: text };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await apiFetch('/api/drop/upload', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await res.json();
        // Report success the same way the file path does: destination, and
        // verified only when the server actually confirmed it. The previous
        // behaviour logged the outcome and showed nothing on this tab, so a
        // successful text send looked identical to a dead button.
        if (res.ok && data.status === 'success') {
          textarea.value = '';
          const dest = data.destination || destinationDisplayName();
          const op = data.operation_id ? ' (ID ' + data.operation_id + ')' : '';
          banner.style.display = 'block';
          banner.className = 'status-banner success';
          banner.textContent = 'Text sent to ' + dest + (data.verified ? ' · verified' : '') + op;
          addLog('DROP', 'Text snippet sent to ' + dest + op + '.');
        } else {
          let msg = data.plain_message || data.message || 'Unknown error';
          if (data.next_action) msg += ' Next: ' + data.next_action;
          if (data.duplicate_risk) msg = 'Text may already be saved. ' + msg;
          if (data.operation_id) msg += ' (ID ' + data.operation_id + ')';
          banner.style.display = 'block';
          banner.className = 'status-banner error';
          // The draft is intentionally kept so a recoverable failure does not
          // silently discard what the user wrote.
          banner.textContent = 'Not sent: ' + msg;
          addLog('ERROR', 'Failed to send text: ' + msg);
        }
        loadTransfers();
        updateNextAction();
      } catch (err) {
        banner.style.display = 'block';
        banner.className = 'status-banner error';
        banner.textContent = 'Not sent: ' + err.message + ' Your text is still here.';
        addLog('ERROR', 'Text send failed: ' + err.message);
      } finally {
        btn.disabled = false;
      }
    }

    async function relayOAuth(e) {
      e.preventDefault();
      const input = document.getElementById('oauthUrlInput');
      const btn = document.getElementById('btnRelay');
      const status = document.getElementById('relayStatus');
      const url = input.value.trim();
      if (!url) return;

      btn.disabled = true;
      status.className = 'status-banner relaying';
      status.textContent = '⏳ Relaying OAuth callback through bridge...';
      addLog('OAUTH', 'Forwarding URL: ' + redactForLog(url));

      const payload = { url: url };
      if (selectedPeerTarget) {
        payload.peer = selectedPeerTarget;
      }

      try {
        const res = await apiFetch('/api/relay/open', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          status.className = 'status-banner success';
          status.textContent = 'Authentication completed successfully' + (data.destination ? ' via ' + data.destination : '') + '.';
          input.value = '';
          addLog('OAUTH', 'OAuth flow completed successfully' + (data.destination ? ' via ' + data.destination : '') + '.');
        } else {
          let msg = data.message || 'Unknown error';
          if (data.next_action) msg += ' Next: ' + data.next_action;
          if (data.operation_id) msg += ' (ID ' + data.operation_id + ')';
          msg += ' See Recent Authorizations below for details.';
          status.className = 'status-banner error';
          status.textContent = 'Relay failed: ' + msg;
          addLog('ERROR', 'Relay failed: ' + msg);
        }
        loadAuthorizations();
      } catch (err) {
        status.className = 'status-banner error';
        status.textContent = '❌ Connection error: ' + err.message;
        addLog('ERROR', 'Relay error: ' + err.message);
        loadAuthorizations();
      } finally {
        btn.disabled = false;
      }
    }

    function copySnippet(text, btn) {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(onSuccess, fallbackCopy);
      } else {
        fallbackCopy();
      }

      function fallbackCopy() {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try {
          document.execCommand('copy');
          onSuccess();
        } catch (_) {}
        document.body.removeChild(ta);
      }

      function onSuccess() {
        const oldText = btn.innerHTML;
        btn.innerHTML = '✅ Copied!';
        btn.style.color = '#34d399';
        btn.style.borderColor = '#10b981';
        setTimeout(() => {
          btn.innerHTML = oldText;
          btn.style.color = '';
          btn.style.borderColor = '';
        }, 2000);
      }
    }

    function openURL(url) {
      try {
        const parsed = new URL(url);
        if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return;
        window.open(parsed.href, '_blank', 'noopener,noreferrer');
      } catch (_) {}
    }

    async function openDownloadFolder() {
      try {
        const res = await apiFetch('/api/open-folder', { method: 'POST' });
        const data = await res.json();
        if (!res.ok || data.status !== 'success') {
          alert('Failed to open folder: ' + (data.message || 'unknown error'));
        }
      } catch (err) {
        alert('Error opening folder: ' + err.message);
      }
    }

    async function promptChangeDownloadDir() {
      const current = document.getElementById('downloadDirPath').innerText;
      const newDir = prompt('Enter new downloads directory path:', current);
      if (!newDir || newDir.trim() === '' || newDir.trim() === current) return;
      try {
        const res = await apiFetch('/api/config', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ output_dir: newDir.trim() })
        });
        const data = await res.json();
        if (res.ok && data.status === 'success') {
          document.getElementById('downloadDirPath').innerText = data.output_dir || newDir.trim();
          addLog('DROP', 'Downloads directory changed. Previously received files stay where they were.');
          alert('Downloads directory updated. Files received earlier stay in their previous location.');
        } else {
          alert('Failed to update directory: ' + (data.message || 'unknown error'));
        }
      } catch (err) {
        alert('Error updating directory: ' + err.message);
      }
    }

    async function loadConfig() {
      try {
        const res = await apiFetch('/api/config');
        if (res.ok) {
          const cfg = await res.json();
          if (cfg.output_dir) {
            document.getElementById('downloadDirPath').innerText = cfg.output_dir;
          }
        }
      } catch (_) {}
    }

    let recentDropsReloadInFlight = false;
    let recentDropsReloadQueued = false;
    async function scheduleRecentDropsReload() {
      if (recentDropsReloadInFlight) {
        recentDropsReloadQueued = true;
        return;
      }
      recentDropsReloadInFlight = true;
      try {
        await loadRecentDrops();
      } finally {
        recentDropsReloadInFlight = false;
        if (recentDropsReloadQueued) {
          recentDropsReloadQueued = false;
          void scheduleRecentDropsReload();
        }
      }
    }

    async function loadRecentDrops() {
      try {
        const res = await apiFetch('/api/drop/recent');
        if (!res.ok) return;
        const items = await res.json();
        renderRecentDrops(items);
      } catch (_) {}
    }

    function renderRecentDrops(items) {
      const list = document.getElementById('receivedDropsList');
      if (!list) return;
      // The inbox is re-rendered wholesale whenever a DROP event arrives.
      // Capture what the user was on so an arriving file does not throw a
      // keyboard or screen-reader user back to the top of the tab.
      const active = document.activeElement;
      // Match the control by action, label and ordinal position. The
      // data-value is a per-render sequence number (receivedActionValues is
      // reset on every render), so it cannot identify the control across
      // renders; the label and position can.
      const controls = Array.prototype.slice.call(list.querySelectorAll('[data-action]'));
      const activeIndex = (active && list.contains(active)) ? controls.indexOf(active) : -1;
      const restoreAction = activeIndex >= 0 ? active.getAttribute('data-action') : null;
      const restoreLabel = activeIndex >= 0 ? (active.textContent || '').trim() : null;
      const previousCount = list.querySelectorAll('.received-item').length;
      receivedActionValues = Object.create(null);
      if (!items || items.length === 0) {
        list.innerHTML = '<p style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 2rem 0;">No received items yet.<br>Snippets and files sent by your peer appear here in real-time.<br>Session items — cleared when the Hub restarts. Sent transfers live under Transfers.</p>';
        announceInboxArrival(0, previousCount);
        return;
      }
      const reversed = items.slice().reverse();
      list.innerHTML = reversed.map(function(item) {
        const copyID = rememberReceivedValue(item.content || '');
        const urlID = rememberReceivedValue(item.content || '');
        const timeStr = formatRecordTime(item.timestamp);
        const isFile = item.kind === 'file';
        const isURL = item.is_url;
        const icon = isFile ? '📁' : (isURL ? '🌐' : '📝');
        const title = isFile ? (item.name || 'Received File') : (isURL ? 'Web URL' : 'Text Snippet');
        const safeTitle = escapeHTML(title);
        const sizeStr = formatBytes(item.size);
        // A loopback self-send is labelled "Unauthenticated Peer" by the
        // receiver, which reads as an unknown third party. Say what happened.
        let fromPeer = item.from_peer || 'Peer';
        if (/^unauthenticated peer$/i.test(fromPeer)) {
          fromPeer = lastTransport === 'loopback' ? 'this Hub (local loopback)' : 'a peer that did not identify itself';
        }

        let contentHTML = '';
        if (isFile) {
          const fileURL = item.id ? '/api/drop/file?id=' + encodeURIComponent(item.id) : '';
          const extMatch = (item.name || '').toLowerCase().match(/\.[a-z0-9]+$/);
          const imgExts = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.bmp', '.avif', '.ico'];
          const previewable = fileURL && extMatch && imgExts.indexOf(extMatch[0]) !== -1 && item.size > 0 && item.size <= 8 * 1024 * 1024;
          if (previewable) {
            contentHTML = '<img class="received-item-preview" src="' + fileURL + '&mode=inline" alt="' + escapeHTML('Preview of ' + (item.name || 'received image')) + '" loading="lazy">' +
              '<div class="received-item-content">Path: ' + escapeHTML(item.saved_path || item.name) + ' (' + sizeStr + ')</div>';
          } else {
            contentHTML = '<div class="received-item-content">Path: ' + escapeHTML(item.saved_path || item.name) + ' (' + sizeStr + ')</div>';
          }
        } else {
          contentHTML = '<div class="received-item-content">' + escapeHTML(item.content || '') + '</div>';
        }

        let actionsHTML = '<div class="received-item-actions">';
        if (!isFile && item.content) {
          actionsHTML += '<button class="btn-sm" data-action="copy-snippet" data-value="' + escapeHTML(copyID) + '">📋 Copy</button>';
          if (isURL || item.content.startsWith('http://') || item.content.startsWith('https://')) {
            const cleanURL = item.content.trim();
            actionsHTML += '<button class="btn-sm" data-action="open-url" data-value="' + escapeHTML(urlID) + '">🌐 Open in Browser</button>';
          }
        } else if (isFile && item.saved_path) {
          if (item.id) {
            actionsHTML += '<a class="btn-sm" href="/api/drop/file?id=' + encodeURIComponent(item.id) + '&mode=download">⬇️ Download</a>';
          }
          actionsHTML += '<button class="btn-sm" data-action="open-folder">📂 Open in Folder</button>';
        }
        actionsHTML += '</div>';

        return '<div class="received-item">' +
          '<div class="received-item-header">' +
            '<span><strong>' + icon + ' ' + safeTitle + '</strong> from ' + escapeHTML(fromPeer) + '</span>' +
            '<span>' + timeStr + ' &bull; ' + sizeStr + '</span>' +
          '</div>' +
          contentHTML +
          actionsHTML +
        '</div>';
      }).join('');
      // Put the user back on the control they were using, matched by action and
      // value rather than node identity, then announce genuine arrivals.
      if (restoreAction) {
        const candidates = Array.prototype.slice.call(list.querySelectorAll('[data-action]'));
        const sameLabel = candidates.filter(function(el) {
          return el.getAttribute('data-action') === restoreAction && (el.textContent || '').trim() === restoreLabel;
        });
        const match = sameLabel[activeIndex] || candidates[activeIndex] || sameLabel[0];
        if (match) {
          try { match.focus(); } catch (_) {}
        }
      }
      announceInboxArrival(list.querySelectorAll('.received-item').length, previousCount);
    }

    // Arrivals are a status change, so they are announced once, politely, and
    // only when the inbox actually grew.
    function announceInboxArrival(newCount, previousCount) {
      const status = document.getElementById('inboxStatus');
      if (!status) return;
      const added = newCount - previousCount;
      status.textContent = added > 0
        ? (added === 1 ? '1 new item received.' : added + ' new items received.')
        : '';
    }

    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
      let value = bytes;
      let unit = 0;
      while (value >= 1024 && unit < units.length - 1) {
        value /= 1024;
        unit++;
      }
      return (unit === 0 ? String(Math.floor(value)) : value.toFixed(1)) + ' ' + units[unit];
    }

    function redactForLog(rawURL) {
      try {
        const parsed = new URL(rawURL);
        parsed.username = '';
        parsed.password = '';
        parsed.hash = '';
        if (parsed.search) parsed.search = '?redacted';
        return parsed.toString();
      } catch (_) {
        return '<invalid-url>';
      }
    }

    function escapeHTML(str) {
      return String(str).replace(/[&<>'"]/g, tag => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        "'": '&#39;',
        '"': '&quot;'
      }[tag] || tag));
    }

    measureChrome();

    dashboardReady.then((ready) => {
      if (!ready) {
        document.getElementById('sessionBanner').style.display = 'block';
        return;
      }
      // SSE + polling start here; HttpOnly session cookies ride automatically.
      if (window.EventSource) {
        const sse = new EventSource('/api/events', { withCredentials: true });
        sse.onmessage = (e) => {
          try {
            const item = JSON.parse(e.data);
            const domain = item.domain || item.tag || 'SYS';
            addLog(domain, item.message || JSON.stringify(item), item.level, item.metadata);
            if (domain === 'DROP') {
              scheduleRecentDropsReload();
            }
          } catch (_) {
            addLog('SYS', e.data, 'INFO');
          }
        };
        sse.onerror = () => { /* reconnects automatically */ };
      }

      setInterval(updateStatus, 3000);
      updateStatus();
      loadConfig();
      loadRecentDrops();
      loadTransfers();
      loadAuthorizations();
      loadInitialLogs();
    });
  </script>
</body>
</html>`

type relayResponse struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id,omitempty"`
	Destination string `json:"destination,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
}

func newDashboardNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// javascriptCSPHash authorizes the dashboard's user-activated bookmarklet
// without reopening script-src to unsafe-inline. CSP hashes the javascript:
// navigation's script body; the URL itself is generated per response because
// it carries a one-use relay ticket.
func javascriptCSPHashes(raw string) string {
	source := strings.TrimPrefix(raw, "javascript:")
	sumSource := sha256.Sum256([]byte(source))
	sumFull := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("'sha256-%s' 'sha256-%s'", base64.StdEncoding.EncodeToString(sumSource[:]), base64.StdEncoding.EncodeToString(sumFull[:]))
}

func escapeHTMLText(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(s)
}

func isAllowedOrigin(originHeader string) bool {
	return isAllowedOriginForHost(originHeader, "")
}

func isAllowedOriginForHost(originHeader, requestHost string) bool {
	if originHeader == "" || strings.EqualFold(originHeader, "null") {
		return originHeader == ""
	}
	u, err := url.Parse(originHeader)
	if err != nil || u.User != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	if requestHost == "" {
		return true
	}
	requestName, requestPort, splitErr := net.SplitHostPort(requestHost)
	if splitErr != nil {
		requestName = strings.Trim(requestHost, "[]")
		requestPort = ""
	}
	if requestName != "" {
		requestName = strings.ToLower(requestName)
		if requestName != "127.0.0.1" && requestName != "localhost" && requestName != "::1" {
			return false
		}
	}
	originPort := u.Port()
	if originPort == "" {
		if u.Scheme == "https" {
			originPort = "443"
		} else {
			originPort = "80"
		}
	}
	if requestPort == "" {
		return originPort == "80" || originPort == "443"
	}
	return originPort == requestPort
}

func requestHostIsLoopbackHeader(hostHeader string) bool {
	host := strings.TrimSpace(hostHeader)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		host = strings.Trim(host, "[]")
	}
	host = strings.ToLower(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func canonicalLoopbackHost(host string) string {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if host == "localhost" {
		return "127.0.0.1"
	}
	return host
}

func splitAuthority(authority string) (host, port string, ok bool) {
	authority = strings.TrimSpace(authority)
	if authority == "" {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return "", "", false
	}
	host = canonicalLoopbackHost(host)
	if host != "127.0.0.1" && host != "::1" {
		return "", "", false
	}
	if port == "" {
		return "", "", false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", "", false
	}
	return host, port, true
}

func sameLoopbackAuthority(actual, candidate string) bool {
	actualHost, actualPort, actualOK := splitAuthority(actual)
	candidateHost, candidatePort, candidateOK := splitAuthority(candidate)
	return actualOK && candidateOK && actualHost == candidateHost && actualPort == candidatePort
}

func isAllowedOriginForActual(originHeader, actualAuthority string) bool {
	if originHeader == "" || strings.EqualFold(originHeader, "null") {
		return false
	}
	u, err := url.Parse(originHeader)
	if err != nil || u.User != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return sameLoopbackAuthority(actualAuthority, u.Host)
}

func (h *Hub) requestHasDashboardCapability(r *http.Request) bool {
	if r == nil {
		return false
	}
	ipcToken := h.currentIPCToken()
	if ipcToken != "" {
		provided := r.Header.Get(IPCTokenHeader)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(ipcToken)) == 1 {
			return true
		}
	}
	if cookie, err := r.Cookie("tantu_dashboard_session"); err == nil && h.dashboardSessionValid(cookie.Value) {
		return true
	}
	return false
}

func (h *Hub) consumeDashboardBootstrap(r *http.Request) bool {
	if r == nil {
		return false
	}
	provided := strings.TrimSpace(r.Header.Get("X-Tantu-Dashboard-Bootstrap"))
	if provided == "" {
		return false
	}
	h.dashboardMu.Lock()
	expected := h.dashboardBootstrapToken
	if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		h.dashboardMu.Unlock()
		return false
	}
	// A bootstrap value is one-use. It is removed before allocating a session,
	// so concurrent requests cannot exchange it twice.
	h.dashboardBootstrapToken = ""
	h.dashboardMu.Unlock()
	return true
}

func (h *Hub) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")

		// Validate the complete authority, including its port. Checking only
		// the hostname lets a raw client forge a different local authority and
		// makes Origin validation depend on an attacker-controlled Host value.
		actualWeb := h.WebAddr()
		if actualWeb == "" || !sameLoopbackAuthority(actualWeb, r.Host) {
			http.Error(w, "local-only request rejected", http.StatusForbidden)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && !isAllowedOriginForActual(origin, actualWeb) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"status":  "error",
					"message": "forbidden cross-origin request",
				})
				return
			}
			referer := r.Header.Get("Referer")
			if referer != "" {
				refererURL, err := url.Parse(referer)
				if err != nil || !isAllowedOriginForActual(refererURL.Scheme+"://"+refererURL.Host, actualWeb) {
					writeJSON(w, http.StatusForbidden, map[string]string{
						"status":  "error",
						"message": "forbidden cross-origin request",
					})
					return
				}
			}

			// The bootstrap exchange is the only unauthenticated API operation.
			// It consumes a one-time value delivered in a browser URL fragment
			// and returns an HttpOnly, same-site session cookie.
			if r.URL.Path == "/api/session" {
				if r.Method != http.MethodPost {
					writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
					return
				}
				if !h.consumeDashboardBootstrap(r) {
					writeJSON(w, http.StatusForbidden, map[string]string{"status": "error", "message": "dashboard bootstrap capability required"})
					return
				}
				sessionID, err := h.newDashboardSession()
				if err != nil {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "dashboard session unavailable"})
					return
				}
				http.SetCookie(w, &http.Cookie{
					Name:     "tantu_dashboard_session",
					Value:    sessionID,
					Path:     "/",
					MaxAge:   1800,
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}

			if r.Method != http.MethodOptions && !h.requestHasDashboardCapability(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"status":  "error",
					"message": "dashboard session or local IPC capability required",
				})
				return
			}
			// CLI/Hub version skew is otherwise invisible: delegation sends
			// X-CLI-Version on every IPC call but nothing reads it. Log the
			// mismatch (deduped per version per Hub run) so mixed-version
			// API drift shows up in diagnostics instead of surfacing as
			// confusing downstream errors. Mixed versions remain best-effort.
			if cliVer := strings.TrimSpace(r.Header.Get("X-CLI-Version")); cliVer != "" && CanonicalVersion(cliVer) != CanonicalVersion(HubVersion) && h.logger != nil {
				h.dashboardMu.Lock()
				alreadyWarned := h.skewWarnedFor == cliVer
				if !alreadyWarned {
					h.skewWarnedFor = cliVer
				}
				h.dashboardMu.Unlock()
				if !alreadyWarned {
					h.logger.Warn(DomainSys, fmt.Sprintf("CLI/Hub version skew: caller %q vs hub %q on %s %s", cliVer, HubVersion, r.Method, r.URL.Path))
				}
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token, X-Tantu-Dashboard-Bootstrap")
				w.Header().Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func publicRelayError(err error) string {
	if err == nil {
		return "relay flow failed"
	}
	return "relay flow failed"
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

const maxControlBodySize = 64 * 1024

func boundControlBody(w http.ResponseWriter, r *http.Request, maxBytes int64) func() {
	if r == nil || r.Body == nil {
		return func() {}
	}
	if maxBytes <= 0 {
		maxBytes = maxControlBodySize
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	// ReadHeaderTimeout does not cover a slow body. Control requests are
	// small, so give them a short absolute read deadline while leaving the
	// long-lived upload endpoint's transfer budget intact. Clear it before
	// returning so a keep-alive connection is not poisoned for its next
	// request.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	return func() { _ = controller.SetReadDeadline(time.Time{}) }
}

type statusResponse struct {
	Status          string         `json:"status"`
	Version         string         `json:"version"`
	Identity        identityStatus `json:"identity"`
	Transport       string         `json:"transport"`
	P2PAddr         string         `json:"p2p_addr"`
	WebAddr         string         `json:"web_addr"`
	ActivePeer      string         `json:"active_peer,omitempty"`
	DiscoveredCount int            `json:"discovered_count"`
	DiscoveredPeers int            `json:"discovered_peers"`
	Peers           []peerStatus   `json:"peers"`
	PID             int            `json:"pid,omitempty"`
	StartedAt       time.Time      `json:"started_at,omitempty"`
}

type probeStatusResponse struct {
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	Transport string    `json:"transport"`
	WebAddr   string    `json:"web_addr"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type discoveredPeerResponse struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	SAS         string `json:"sas"`
	Fingerprint string `json:"fingerprint"`
	IsPaired    bool   `json:"is_paired"`
}

type identityStatus struct {
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
}

type peerStatus struct {
	Name        string `json:"name"`
	Alias       string `json:"alias,omitempty"`
	IsDefault   bool   `json:"is_default,omitempty"`
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	SAS         string `json:"sas"`
	Active      bool   `json:"active"`
}

// relayInterstitialHTML is the bookmarklet confirmation shell. It carries
// no credential and authorizes nothing: the full authorization URL reaches
// the relay endpoint only through an explicit same-origin POST after a click.
// Dynamic values enter via textContent or a JSON-encoded literal, never via
// HTML interpolation of untrusted content.
const relayInterstitialHTML = `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Confirm OAuth Relay</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;-webkit-font-smoothing:antialiased;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{margin:0 0 0.5rem 0;letter-spacing:-0.01em;}h2.ok{color:#34d399;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}p strong{color:#e6edf3;}.note{background:rgba(245,158,11,0.12);border:1px solid rgba(245,158,11,0.4);color:#fbbf24;border-radius:8px;padding:0.6rem 0.8rem;font-size:0.85rem;}.hint{color:#94a3b8;font-size:0.8rem;}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0;}.btn{background:#2563eb;border:none;color:#fff;min-height:44px;padding:0.6rem 1.25rem;border-radius:8px;font-weight:600;font-size:0.9rem;cursor:pointer;box-shadow:0 4px 14px rgba(37,99,235,0.35);transition:transform 150ms ease-out,opacity 150ms ease-out;}.btn:hover:not(:disabled){transform:translateY(-1px);}.btn:disabled{opacity:0.5;cursor:not-allowed;}.btn-ghost{background:transparent;border:1px solid #323c52;color:#e6edf3;box-shadow:none;}button:focus-visible{outline:2px solid #3b82f6;outline-offset:2px;}
/* The Relay button is autofocused on load, and a programmatic focus does not
   match :focus-visible, so the initial focused state had no visible ring at
   all. :focus covers it without double-drawing on pointer interaction. */
button:focus{outline:2px solid #60a5fa;outline-offset:2px;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#ffffff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}p strong{color:#16213a;}.hint{color:#5b6478;}.btn-ghost{border-color:#b9c4d8;color:#16213a;}button:focus{outline-color:#1d4ed8;}}@media (prefers-reduced-motion: reduce){.btn{transition:none;}}</style></head><body><main class="box"><h1 class="sr-only">Confirm OAuth relay</h1><div id="confirmView"><div class="icon">🔐</div><h2>Relay this authorization?</h2><p>From: <strong>{{ORIGIN}}</strong></p>{{NOTICE}}<p class="hint" id="peerLine">Checking destination…</p><div id="noSession" style="display:none" class="hint">Dashboard session not established. Open the dashboard from the Hub terminal (press o) — this popup will keep checking, and enable Relay by itself once the session appears.</div><div style="display:flex;gap:0.5rem;justify-content:center;margin-top:1rem;"><button id="btnRelayNow" class="btn" disabled>Relay now</button><button id="btnCancel" class="btn btn-ghost">Cancel</button></div><div class="hint" id="resultLine" role="status" aria-live="polite" style="margin-top:0.75rem;"></div></div><div id="okView" style="display:none"><div class="icon">✅</div><h2 class="ok">Authentication Relayed!</h2><p id="okText"></p><div class="hint" id="okStatus" role="status" aria-live="polite" style="margin-top:0.5rem;">This window closes automatically in 3 seconds. The login continues on the other machine.</div><div style="display:flex;justify-content:center;margin-top:0.75rem;"><button id="okClose" class="btn btn-ghost">Close now</button></div></div></div></main><script>
const relayURL = new URLSearchParams(window.location.search).get('url') || '';
function show(el) { el.style.display = 'block'; }
function hide(el) { el.style.display = 'none'; }
let statusTimer = null;
let sessionWasReady = false;
async function refreshSessionState() {
  const peerLine = document.getElementById('peerLine');
  const noSession = document.getElementById('noSession');
  const btnRelay = document.getElementById('btnRelayNow');
  const resultLine = document.getElementById('resultLine');
  if (!relayURL) {
    peerLine.textContent = 'No authorization URL found. Start from the OAuth login page and retry the bookmark.';
    return;
  }
  let authed = false;
  let data = null;
  try {
    const res = await fetch('/api/status', { credentials: 'same-origin' });
    if (res.ok) {
      authed = true;
      data = await res.json();
    }
  } catch (_) {}
  // Readiness is the HTTP result, not the payload shape: an authenticated
  // Hub with zero peers answers 200 with peers:null, which is a valid
  // session that must enable the button.
  if (!authed || data == null || typeof data !== 'object') {
    // No session (or Hub unreachable): guidance stays up and the button
    // stays off. If a session appears later — e.g. the dashboard was just
    // opened in another tab — the next poll enables everything by itself.
    if (sessionWasReady) {
      sessionWasReady = false;
      btnRelay.disabled = true;
      resultLine.textContent = '';
    }
    hide(peerLine);
    show(noSession);
    return;
  }
  const peers = Array.isArray(data.peers) ? data.peers : [];
  let dest = 'Default peer';
  if (peers.length === 1) { dest = peers[0].name || 'Peer'; }
  else if (peers.length > 1) {
    const sel = peers.filter(function(p) { return p.active; })[0] || peers[0];
    dest = (sel && sel.name) || 'Peer';
  } else if ((data.transport || 'loopback') === 'loopback') { dest = 'this Hub (local)'; }
  else { dest = 'No trusted peers yet'; btnRelay.disabled = true; peerLine.textContent = 'Destination: ' + dest + ' — pair a peer first.'; return; }
  show(peerLine);
  hide(noSession);
  peerLine.textContent = 'Destination: ' + dest;
  btnRelay.disabled = false;
  if (!sessionWasReady) {
    // Transition only: announce once and focus, never steal focus or
    // repeat announcements on routine polls.
    sessionWasReady = true;
    resultLine.textContent = 'Dashboard session established. Relay button enabled.';
    btnRelay.focus();
  }
}
async function boot() {
  await refreshSessionState();
  statusTimer = setInterval(refreshSessionState, 3000);
}
function closeSelf() {
  // window.close() is a no-op for a tab the script did not open, which left
  // the user staring at a popup that refused to do anything. Say so instead.
  window.close();
  setTimeout(function() {
    const hint = document.getElementById('resultLine');
    if (hint) {
      hint.textContent = 'This window cannot close itself. Close the tab to continue.';
    }
  }, 250);
}
document.getElementById('btnCancel').addEventListener('click', closeSelf);
document.getElementById('okClose').addEventListener('click', closeSelf);
document.getElementById('btnRelayNow').addEventListener('click', async function() {
  const btnRelay = document.getElementById('btnRelayNow');
  const resultLine = document.getElementById('resultLine');
  btnRelay.disabled = true;
  resultLine.textContent = 'Relaying…';
  try {
    const res = await fetch('/api/relay/open', { method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'same-origin', body: JSON.stringify({ url: relayURL }) });
    let data = {};
    try { data = await res.json(); } catch (_) {}
    if (res.ok && data.status === 'success') {
      // The success text lives outside the hidden confirm view, and the view
      // carries its own live region: resultLine is inside confirmView, so a
      // success was previously rendered into a display:none subtree and never
      // announced at all.
      hide(document.getElementById('confirmView'));
      const okText = document.getElementById('okText');
      okText.textContent = 'Authentication relayed' + (data.destination ? ' to the waiting terminal via ' + data.destination : '') + '. Return to that terminal to continue.' + (data.operation_id ? ' (ID ' + data.operation_id + ')' : '');
      show(document.getElementById('okView'));
      if (statusTimer != null) { clearInterval(statusTimer); statusTimer = null; }
      const okBtn = document.getElementById('okClose');
      if (okBtn) okBtn.focus();
      setTimeout(function() { window.close(); }, 3000);
    } else {
      if (res.status === 401) {
        sessionWasReady = false;
        btnRelay.disabled = true;
        hide(document.getElementById('peerLine'));
        show(document.getElementById('noSession'));
      } else if (res.status >= 500) {
        // A server fault is not a missing session. Reporting it as one sent
        // users off to re-mint a dashboard link for a problem a retry fixed.
        resultLine.textContent = 'The Hub could not complete the relay (HTTP ' + res.status + '). Nothing was relayed. Try again in a moment.';
        btnRelay.disabled = false;
        return;
      }
      let msg = (data && data.message) || 'Relay failed';
      if (data && data.next_action) msg += ' Next: ' + data.next_action;
      if (data && data.operation_id) msg += ' (ID ' + data.operation_id + ')';
      resultLine.textContent = msg;
      if (res.status !== 401) {
        btnRelay.disabled = false;
      }
    }
  } catch (err) {
    resultLine.textContent = 'Connection error: ' + err.message;
    btnRelay.disabled = false;
  }
});
boot();
</script></body></html>`

// renderRelayInterstitial serves the bookmarklet confirmation shell for a
// relay request that carries no credential. It authorizes nothing: the page
// shows only the safe origin host and offers an explicit Relay button, which
// POSTs same-origin so the dashboard session flows. Without a click and a
// valid session, no relay happens. expiredLink notes a previously-ticketed
// bookmark and explains the extra step instead of dead-ending.
func (h *Hub) renderRelayInterstitial(w http.ResponseWriter, rawURL string, expiredLink bool) {
	sanitized := browser.SanitizeURL(strings.TrimSpace(rawURL))
	renderErr := func() {
		// A dead end is not an error message. This page is reached by a bad or
		// non-relayable link, so it names the reason, the one safe next step,
		// and a keyboard-reachable way back to the dashboard.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;padding:1rem;box-sizing:border-box;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}h1{margin:0 0 0.5rem 0;font-size:1.1rem;}p{color:#94a3b8;font-size:0.9rem;margin:0 0 1rem 0;line-height:1.5;}.btn{display:inline-block;background:#2563eb;color:#fff;min-height:44px;padding:0.6rem 1.25rem;border-radius:8px;font-weight:600;font-size:0.9rem;text-decoration:none;}.btn:focus{outline:2px solid #60a5fa;outline-offset:2px;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#fff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}}@media (prefers-reduced-motion: reduce){*{transition:none!important;}}</style></head><body><div class="box"><h1>This link cannot be relayed</h1><p>Only an http or https authorization URL can be relayed, and this link did not contain one. Nothing was sent to any machine.</p><p>Go back to the OAuth login page and click the Tantu bookmark again, or open the dashboard and use the manual forwarder there.</p><a class="btn" href="/">Open the dashboard</a></div></body></html>`))
	}
	if err := browser.ValidateURL(sanitized); err != nil {
		renderErr()
		return
	}
	origin := ""
	if u, err := url.Parse(sanitized); err == nil {
		origin = u.Hostname()
	}
	if origin == "" {
		renderErr()
		return
	}
	notice := ""
	if expiredLink {
		notice = `<p class="note">This saved link already expired — confirm below to continue. Your bookmark still works; nothing needs re-saving.</p>`
	}
	page := relayInterstitialHTML
	page = strings.ReplaceAll(page, "{{ORIGIN}}", escapeHTMLText(origin))
	page = strings.ReplaceAll(page, "{{NOTICE}}", notice)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

// registerDashboardRoutes configures all routes for the Single-Page Web Dashboard and APIs.
func (h *Hub) registerDashboardRoutes(mux *http.ServeMux) {
	// 1. GET / — Unified Dashboard SPA
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		nonce, nonceErr := newDashboardNonce()
		if nonceErr != nil {
			http.Error(w, "dashboard security nonce unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		webAddr := h.WebAddr()
		if webAddr == "" {
			h.mu.RLock()
			webAddr = h.cfg.WebAddr
			h.mu.RUnlock()
		}
		bookmarkletJS := "javascript:void(0)"
		if h.requestHasDashboardCapability(r) {
			// The bookmark carries no credential: opening it renders a
			// confirmation interstitial, and the relay itself still needs an
			// explicit click plus a valid dashboard session at POST time.
			// Nothing here expires, so a saved bookmark keeps working.
			bookmarkletJS = fmt.Sprintf("javascript:void(window.open('http://%s/relay?url='+encodeURIComponent(location.href),'_blank','width=550,height=380'))", webAddr)
		}
		// `unsafe-hashes` is limited to the exact generated bookmarklet
		// navigation; ordinary inline scripts still require the per-response
		// nonce, and all DOM actions are delegated from that script.
		// img-src allows blob: because the send preview stages the chosen local
		// File as a page-created object URL. A blob: URL is same-origin and
		// scoped to this document's lifetime, so it adds no remote origin and
		// no cross-document read; without it the clipboard-image confirmation
		// card renders an invisible, permanently broken thumbnail and logs a
		// CSP violation on every paste, drop, and file-picker selection.
		w.Header().Set("Content-Security-Policy", fmt.Sprintf("default-src 'self'; script-src 'self' 'nonce-%s' 'unsafe-hashes' %s; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'", nonce, javascriptCSPHashes(bookmarkletJS)))

		page := strings.ReplaceAll(dashboardHTML, "{{VERSION}}", escapeHTMLText(HubVersion))
		page = strings.ReplaceAll(page, "{{BOOKMARKLET_HREF}}", escapeHTMLText(bookmarkletJS))
		// Do not make an unauthenticated page probe every sensitive endpoint
		// just to discover that it needs the one-time bootstrap link. The
		// HttpOnly cookie is intentionally invisible to JavaScript, so the
		// server-side capability check is the authoritative session hint.
		page = strings.ReplaceAll(page, "{{SESSION_READY}}", strconv.FormatBool(h.requestHasDashboardCapability(r)))
		page = strings.ReplaceAll(page, "{{NONCE}}", nonce)
		_, _ = w.Write([]byte(page))
	})

	// 2. GET /healthz
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// 2a. POST /api/session — exchange a one-time browser bootstrap value for
	// an HttpOnly dashboard session. The handler is completed by the security
	// middleware above so the bootstrap value is never accepted by another API.
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		// Kept as an explicit route for documentation and for direct handler
		// tests; the middleware performs the one-time validation and response.
		http.NotFound(w, r)
	})

	// 2b. GET /api/probe — minimal authenticated runtime identity response.
	// Unlike /api/status it contains no identity SAS, peer list, logs, or
	// received content.
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		h.mu.RLock()
		webAddr := h.actualWeb
		transportName := h.cfg.TransportType
		pid := os.Getpid()
		startedAt := h.startTime
		h.mu.RUnlock()
		writeJSON(w, http.StatusOK, probeStatusResponse{
			Status:    "online",
			Version:   HubVersion,
			Transport: transportName,
			WebAddr:   webAddr,
			PID:       pid,
			StartedAt: startedAt,
		})
	})

	// 3. GET /api/status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		h.mu.RLock()
		store := h.store
		transportName := h.cfg.TransportType
		webAddrSnapshot := h.actualWeb
		p2pSnapshot := h.actualP2P
		identitySnapshot := h.identity
		startedAtSnapshot := h.startTime
		engine := h.discoveryEngine
		h.mu.RUnlock()
		activeFP := h.GetActivePeer()

		var peers []peerStatus
		if store != nil {
			allPeers := store.ListPeers()
			for _, p := range allPeers {
				isActive := false
				if activeFP != "" && p.Fingerprint == activeFP {
					isActive = true
				} else if activeFP == "" && (p.IsDefault || len(allPeers) == 1) {
					isActive = true
				}
				peers = append(peers, peerStatus{
					Name:        p.DisplayName(),
					Alias:       p.Alias,
					IsDefault:   p.IsDefault,
					Address:     p.Address,
					Fingerprint: p.Fingerprint,
					SAS:         p.SAS(),
					Active:      isActive,
				})
			}
		}

		discCount := 0
		if engine != nil {
			discCount = len(engine.ListNodes())
		}

		resp := statusResponse{
			Status:          "online",
			Version:         HubVersion,
			Transport:       transportName,
			WebAddr:         webAddrSnapshot,
			ActivePeer:      activeFP,
			DiscoveredCount: discCount,
			DiscoveredPeers: discCount,
			Peers:           peers,
			PID:             os.Getpid(),
			StartedAt:       startedAtSnapshot,
		}
		if p2pSnapshot != nil {
			resp.P2PAddr = p2pSnapshot.String()
		}
		if identitySnapshot != nil {
			resp.Identity = identityStatus{
				Fingerprint: identitySnapshot.Fingerprint,
				SAS:         pairing.SASCode(identitySnapshot.Fingerprint),
			}
		}

		writeJSON(w, http.StatusOK, resp)
	})

	// 4. GET /relay — Legacy bookmarklet handler
	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status:  "error",
				Message: "method not allowed, use GET",
			})
			return
		}

		respond := func(status int, resp relayResponse) {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(status)
				if resp.Status == "success" {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>✅ Auth Relayed</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;-webkit-font-smoothing:antialiased;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#34d399;margin:0 0 0.5rem 0;letter-spacing:-0.01em;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#ffffff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}.hint{color:#7b8499;}}</style></head><body><div class="box"><div class="icon">✅</div><h2>Authentication Relayed!</h2><p>%s</p><div class="hint">This window will close automatically in 3 seconds...</div></div><script>setTimeout(function(){window.close();},3000);</script></body></html>`, escapeHTMLText(resp.Message))
				} else {
					fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>❌ Relay Error</title><style>body{background:#090b10;color:#e6edf3;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;-webkit-font-smoothing:antialiased;}.box{background:#131722;border:1px solid #232a3b;border-radius:16px;padding:2rem;max-width:460px;width:92%%;text-align:center;box-shadow:0 25px 50px -12px rgba(0,0,0,0.5);}.icon{font-size:2.5rem;margin-bottom:0.75rem;}h2{color:#f87171;margin:0 0 0.5rem 0;letter-spacing:-0.01em;}p{color:#8b949e;font-size:0.9rem;margin:0 0 1rem 0;}.hint{color:#64748b;font-size:0.8rem;}@media (prefers-color-scheme: light){body{background:#f4f6fb;color:#16213a;}.box{background:#ffffff;border-color:#d9e0ec;box-shadow:0 25px 50px -12px rgba(22,33,58,0.25);}p{color:#5b6478;}.hint{color:#7b8499;}}</style></head><body><div class="box"><div class="icon">❌</div><h2>Relay Error</h2><p>%s</p><div class="hint">Check terminal logs or verify your peer is connected.</div></div></body></html>`, escapeHTMLText(resp.Message))
				}
				return
			}
			writeJSON(w, status, resp)
		}

		rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
		if rawURL == "" {
			// Keep the legacy endpoint's harmless validation response readable
			// for old callers; no relay work is performed without a URL.
			respond(http.StatusBadRequest, relayResponse{
				Status:  "error",
				Message: "missing or empty url parameter",
			})
			return
		}
		// The legacy GET endpoint deliberately does not accept the browser
		// session cookie as relay authorization: a cross-site top-level
		// navigation must not be able to turn that cookie into a relay
		// capability. Authenticated dashboard mutations use POST
		// /api/relay/open. Requests without an explicit credential render a
		// confirmation interstitial instead of relaying: the shell authorizes
		// nothing, and the relay itself still requires an explicit click plus
		// a valid session at POST time. A previously-ticketed bookmark whose
		// ticket is gone degrades to the same page instead of dead-ending.
		ipcToken := h.currentIPCToken()
		relayAuthorized := ipcToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(IPCTokenHeader)), []byte(ipcToken)) == 1
		if !relayAuthorized {
			providedRelayToken := r.URL.Query().Get("token")
			relayToken := h.currentRelayToken()
			relayAuthorized = relayToken != "" && subtle.ConstantTimeCompare([]byte(providedRelayToken), []byte(relayToken)) == 1
		}
		if !relayAuthorized {
			h.renderRelayInterstitial(w, rawURL, strings.TrimSpace(r.URL.Query().Get("ticket")) != "")
			return
		}

		attemptID, err := h.relayOAuth(r.Context(), "", rawURL)
		if err != nil {
			dest, _, _ := h.resolveOperationDestination("")
			respond(http.StatusBadGateway, relayResponse{
				Status:      "error",
				Message:     publicRelayError(err),
				OperationID: attemptID,
				Destination: dest,
				NextAction:  h.relayAttemptNextAction(attemptID),
			})
			return
		}

		dest, _, _ := h.resolveOperationDestination("")
		respond(http.StatusOK, relayResponse{
			Status:      "success",
			Message:     "Authentication completed successfully",
			OperationID: attemptID,
			Destination: dest,
		})
	})

	// 5. POST /api/relay/open — Manual URL forwarder
	mux.HandleFunc("/api/relay/open", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, relayResponse{
				Status:  "error",
				Message: "method not allowed, use POST",
			})
			return
		}

		var req struct {
			URL  string `json:"url"`
			Peer string `json:"peer"`
		}
		releaseBody := boundControlBody(w, r, 128*1024)
		defer releaseBody()
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, relayResponse{Status: "error", Message: "invalid json body"})
				return
			}
		} else {
			req.URL = r.FormValue("url")
			req.Peer = r.FormValue("peer")
		}

		rawURL := strings.TrimSpace(req.URL)
		if rawURL == "" {
			writeJSON(w, http.StatusBadRequest, relayResponse{Status: "error", Message: "missing url field"})
			return
		}

		attemptID, err := h.relayOAuth(r.Context(), req.Peer, rawURL)
		if err != nil {
			dest, _, _ := h.resolveOperationDestination(req.Peer)
			writeJSON(w, http.StatusBadGateway, relayResponse{
				Status:      "error",
				Message:     publicRelayError(err),
				OperationID: attemptID,
				Destination: dest,
				NextAction:  h.relayAttemptNextAction(attemptID),
			})
			return
		}

		dest, _, _ := h.resolveOperationDestination(req.Peer)
		writeJSON(w, http.StatusOK, relayResponse{
			Status:      "success",
			Message:     "Authentication completed successfully",
			OperationID: attemptID,
			Destination: dest,
		})
	})

	// 5b. GET/DELETE /api/relay/recent — Sender-side authorization truth
	// (durable: last 50, 30 days; origin hosts only, never URLs, codes, or
	// tokens). DELETE clears the ledger and its file; transfer history and
	// received files are unaffected.
	mux.HandleFunc("/api/relay/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed := h.clearRelayHistory()
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": "Authorization history cleared",
				"removed": removed,
			})
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		ops := h.listRelayAttempts()
		if ops == nil {
			ops = []RelayAttempt{}
		}
		writeJSON(w, http.StatusOK, ops)
	})

	// 6. POST /api/drop/upload — File & Text QuickDrop transfer
	mux.HandleFunc("/api/drop/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}

		contentType := r.Header.Get("Content-Type")

		// JSON text drop
		if strings.HasPrefix(contentType, "application/json") {
			if !acquireSlot(h.textSlots) {
				w.Header().Set("Retry-After", "10")
				writeTransferError(w, http.StatusServiceUnavailable, "too many concurrent uploads, retry later", "hub_busy", "Hub is busy. No data was sent.", "too many concurrent uploads", true, false, true, "Wait a moment and retry. If it persists, try again later.", "", "", "", "")
				return
			}
			defer releaseSlot(h.textSlots)
			var textReq struct {
				Text           string `json:"text"`
				Name           string `json:"name"`
				Peer           string `json:"peer"`
				IdempotencyKey string `json:"idempotency_key"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 11*1024*1024)
			if err := json.NewDecoder(r.Body).Decode(&textReq); err != nil {
				writeTransferError(w, http.StatusBadRequest, "invalid json payload", "invalid_input", "Request was not valid JSON. Nothing was sent.", "invalid json payload", false, false, true, "Fix the request and retry.", "", "", "", "")
				return
			}
			if textReq.Text == "" {
				writeTransferError(w, http.StatusBadRequest, "empty text payload", "invalid_input", "Text was empty. Nothing was sent.", "empty text payload", false, false, true, "Type or paste text, then send again.", "", "", "", "")
				return
			}
			if int64(len(textReq.Text)) > drop.DefaultMaxTextSize {
				writeTransferError(w, http.StatusRequestEntityTooLarge, "text payload exceeds 10 MiB limit", "limit_exceeded", "Text exceeds the 10 MiB limit. Nothing was sent.", "text payload exceeds 10 MiB limit", false, false, true, "Send it as a file instead.", "", "", "", "")
				return
			}
			if textReq.IdempotencyKey != "" && !drop.ValidTombstoneKey(textReq.IdempotencyKey) {
				writeTransferError(w, http.StatusBadRequest, "invalid idempotency key", "invalid_input", "The idempotency key was not a valid identifier. Nothing was sent.", "invalid idempotency key", false, false, true, "Use 1-128 identifier characters (letters, digits, -, _, .) or omit it.", "", "", "", "")
				return
			}

			opID := newOperationID()
			dropID := newOutboundDropID()
			opCreated := time.Now()
			destName, destFP, destAddr := h.resolveOperationDestination(textReq.Peer)
			opKind := "text"
			opName := textReq.Name
			opSize := int64(len(textReq.Text))
			conn, err := h.DialPeer(textReq.Peer)
			if err != nil {
				code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 0, opSize, false)
				opState := operationStateForFailure(retrySafe, duplicateRisk, false)
				h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
				writeTransferError(w, http.StatusBadGateway, fmt.Sprintf("dial peer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
				return
			}
			defer conn.Close()

			// A caller-supplied key makes retried sends duplicate-safe; it was
			// validated above. Otherwise the operation ID is the key.
			wireKey := opID
			if textReq.IdempotencyKey != "" {
				wireKey = textReq.IdempotencyKey
			}
			meta := drop.DropSend{
				DropID:         dropID,
				IdempotencyKey: wireKey,
				Kind:           drop.DropKindText,
				Name:           textReq.Name,
				Size:           int64(len(textReq.Text)),
			}
			sendCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
			defer cancel()

			negotiated := false
			sendCfg := drop.SendDropConfig{Timeout: h.cfg.Timeout}
			sendCfg.OnAck = func(drop.DropAck) { negotiated = true }
			if err = drop.SendDrop(sendCtx, conn, meta, strings.NewReader(textReq.Text), sendCfg); err != nil {
				bytesForClassify := int64(0)
				if negotiated {
					bytesForClassify = opSize
				}
				preNegotiationCancel := !negotiated && (r.Context().Err() != nil || sendCtx.Err() != nil)
				code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, bytesForClassify, opSize, preNegotiationCancel)
				cancelled := preNegotiationCancel
				opState := operationStateForFailure(retrySafe, duplicateRisk, cancelled)
				h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
				writeTransferError(w, http.StatusInternalServerError, fmt.Sprintf("send drop failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
				return
			}

			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateCompleted, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: false, DataSafe: true, NextAction: "", DiagnosticID: opID})
			writeJSON(w, http.StatusOK, map[string]any{
				"status":       "success",
				"message":      "Text drop sent successfully",
				"size":         len(textReq.Text),
				"operation_id": opID,
				"destination":  destName,
				"verified":     true,
			})
			return
		}

		// Multipart file drop (up to 5GB). Slot acquisition comes before any
		// body buffering so rejected uploads cost nothing.
		if !h.acquireUploadSlot() {
			w.Header().Set("Retry-After", "30")
			writeTransferError(w, http.StatusServiceUnavailable, "too many concurrent uploads, retry later", "hub_busy", "Hub is busy. No data was sent.", "too many concurrent uploads", true, false, true, "Wait a moment and retry. If it persists, try again later.", "", "", "", "")
			return
		}
		defer h.releaseUploadSlot()
		r.Body = http.MaxBytesReader(w, r.Body, drop.DefaultMaxDropSize)
		if err := r.ParseMultipartForm(32 * 1024 * 1024); err != nil {
			writeTransferError(w, http.StatusBadRequest, fmt.Sprintf("multipart parse error (max 5GB): %v", err), "invalid_input", "Upload could not be read. Nothing was sent.", fmt.Sprintf("multipart parse error: %v", err), false, false, true, "Check the file and retry.", "", "", "", "")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		file, header, err := r.FormFile("file")
		if err != nil {
			writeTransferError(w, http.StatusBadRequest, "missing file field in form", "invalid_input", "No file was attached. Nothing was sent.", "missing file field in form", false, false, true, "Choose a file, then send again.", "", "", "", "")
			return
		}
		defer file.Close()

		targetPeer := r.FormValue("peer")
		formKey := strings.TrimSpace(r.FormValue("idempotency_key"))
		if formKey != "" && !drop.ValidTombstoneKey(formKey) {
			writeTransferError(w, http.StatusBadRequest, "invalid idempotency key", "invalid_input", "The idempotency key was not a valid identifier. Nothing was sent.", "invalid idempotency key", false, false, true, "Use 1-128 identifier characters (letters, digits, -, _, .) or omit it.", "", "", "", "")
			return
		}
		opID := newOperationID()
		dropID := newOutboundDropID()
		opCreated := time.Now()
		destName, destFP, destAddr := h.resolveOperationDestination(targetPeer)
		opKind := "file"
		opName := filepath.Base(header.Filename)
		opSize := header.Size
		conn, err := h.DialPeer(targetPeer)
		if err != nil {
			code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, 0, opSize, false)
			opState := operationStateForFailure(retrySafe, duplicateRisk, false)
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
			writeTransferError(w, http.StatusBadGateway, fmt.Sprintf("dial peer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
			return
		}
		defer conn.Close()

		fileName := filepath.Base(header.Filename)
		mimeType := mime.TypeByExtension(filepath.Ext(fileName))
		wireKey := opID
		if formKey != "" {
			wireKey = formKey
		}
		meta := drop.DropSend{
			DropID:         dropID,
			IdempotencyKey: wireKey,
			Kind:           drop.DropKindFile,
			Name:           fileName,
			Size:           header.Size,
			MIMEType:       mimeType,
		}
		if seeker, ok := file.(io.ReadSeeker); ok && header.Size >= 64*1024 {
			_, _ = seeker.Seek(0, io.SeekStart)
			head := make([]byte, 64*1024)
			if n, readErr := io.ReadFull(seeker, head); n > 0 && (readErr == nil || readErr == io.EOF || readErr == io.ErrUnexpectedEOF) {
				sum := sha256.Sum256(head[:n])
				meta.HeadHash = hex.EncodeToString(sum[:])
			}
			_, _ = seeker.Seek(0, io.SeekStart)
		}

		sendCtx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
		defer cancel()

		negotiated := false
		sendCfg := drop.SendDropConfig{Timeout: h.cfg.Timeout}
		sendCfg.OnAck = func(drop.DropAck) { negotiated = true }
		if err = drop.SendDrop(sendCtx, conn, meta, file, sendCfg); err != nil {
			bytesForClassify := int64(0)
			if negotiated {
				bytesForClassify = opSize
			}
			preNegotiationCancel := !negotiated && (r.Context().Err() != nil || sendCtx.Err() != nil)
			code, plain, nextAction, retrySafe, duplicateRisk, dataSafe := ClassifyTransferError(err, bytesForClassify, opSize, preNegotiationCancel)
			cancelled := preNegotiationCancel
			opState := operationStateForFailure(retrySafe, duplicateRisk, cancelled)
			h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, MIMEType: mimeType, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: opState, CreatedAt: opCreated, UpdatedAt: time.Now(), RetrySafe: retrySafe, DuplicateRisk: duplicateRisk, DataSafe: dataSafe, ErrorCode: code, ErrorMessage: plain, NextAction: nextAction, DiagnosticID: opID})
			writeTransferError(w, http.StatusInternalServerError, fmt.Sprintf("drop file transfer failed: %v", err), code, plain, err.Error(), retrySafe, duplicateRisk, dataSafe, nextAction, opID, destName, destFP, destAddr)
			return
		}

		h.recordOutboundOperation(OperationRecord{OperationID: opID, DropID: dropID, Kind: opKind, Name: opName, Size: opSize, MIMEType: mimeType, Destination: destName, DestinationFingerprint: destFP, DestinationAddress: destAddr, State: OperationStateCompleted, CreatedAt: opCreated, UpdatedAt: time.Now(), BytesSent: opSize, Verified: true, RetrySafe: false, DuplicateRisk: false, DataSafe: true, DiagnosticID: opID})
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "success",
			"message":      "File drop sent successfully",
			"name":         fileName,
			"size":         header.Size,
			"operation_id": opID,
			"destination":  destName,
			"verified":     true,
		})
	})

	// 7. GET /api/logs — Historical RingBuffer hydration
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if h.logger != nil {
			h.logger.HandleLogs(w, r)
		} else {
			writeJSON(w, http.StatusOK, []any{})
		}
	})

	// 8. GET /api/events — SSE Stream
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if h.logger != nil {
			h.logger.HandleEvents(w, r)
		} else {
			http.Error(w, "logger unavailable", http.StatusServiceUnavailable)
		}
	})

	// 9. GET /api/drop/recent — Returns list of recent received drops
	mux.HandleFunc("/api/drop/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		if h.recentDrops != nil {
			writeJSON(w, http.StatusOK, h.recentDrops.GetAll())
		} else {
			writeJSON(w, http.StatusOK, []ReceivedDropItem{})
		}
	})

	// 9a. GET/DELETE /api/transfers/recent — Sender-side operation truth
	// (durable: last 50, 30 days; metadata only, never payload). DELETE
	// clears the ledger and its file; received files are unaffected.
	mux.HandleFunc("/api/transfers/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			removed := h.clearOutboundOperations()
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "success",
				"message": "Transfer history cleared",
				"removed": removed,
			})
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		ops := h.listOutboundOperations()
		if ops == nil {
			ops = []OperationRecord{}
		}
		writeJSON(w, http.StatusOK, ops)
	})

	// 9b. GET /api/drop/file — Serve one received file for inline preview
	// or download. The file is addressed by drop ID (never by path): the
	// recorded SavedPath must resolve inside the current output directory,
	// be a regular file (never a symlink), and inline rendering is limited
	// to small raster images with a fixed content-type map. SVG, HTML, and
	// everything else are forced to attachment so a malicious peer can never
	// plant active content in the dashboard origin. Capability/session auth
	// is enforced by securityMiddleware like every other /api/* route.
	mux.HandleFunc("/api/drop/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" || len(id) > drop.MaxDropIDLength {
			http.NotFound(w, r)
			return
		}
		if h.recentDrops == nil {
			http.NotFound(w, r)
			return
		}
		item, ok := h.recentDrops.Find(id)
		if !ok || item.Kind != string(drop.DropKindFile) || item.SavedPath == "" {
			http.NotFound(w, r)
			return
		}
		serveReceivedFile(w, r, h.OutputDir(), item)
	})

	// 9c. POST /api/dashboard-url — mint a fresh authenticated dashboard link
	// for local CLI/headless workflows. The response is capability-protected by
	// securityMiddleware and is never cached or logged; the fragment token is
	// one-use and therefore cannot be replayed from shell history.
	mux.HandleFunc("/api/dashboard-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed, use POST"})
			return
		}
		dashboardURL := h.NewDashboardURL()
		if dashboardURL == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "dashboard link unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"url":    dashboardURL,
		})
	})

	// 10. GET /api/config & POST /api/config — Retrieve or update Hub config
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]string{
				"output_dir": h.OutputDir(),
				"transport":  h.TransportType(),
				"web_addr":   h.WebAddr(),
			})
			return
		}
		if r.Method == http.MethodPost {
			releaseBody := boundControlBody(w, r, maxControlBodySize)
			defer releaseBody()
			var req struct {
				OutputDir string `json:"output_dir"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
			req.OutputDir = strings.TrimSpace(req.OutputDir)
			if err := validateOutputDir(req.OutputDir); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": fmt.Sprintf("invalid output directory: %v", err)})
				return
			}
			h.SetOutputDir(req.OutputDir)
			writeJSON(w, http.StatusOK, map[string]string{
				"status":     "success",
				"message":    "configuration updated",
				"output_dir": h.OutputDir(),
			})
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
	})

	// 11. POST /api/open-folder — Launches OS file explorer for output_dir
	mux.HandleFunc("/api/open-folder", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		dir := h.OutputDir()
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("cannot create directory: %v", err)})
			return
		}
		if err := openDirectoryInOS(dir); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": fmt.Sprintf("failed to open folder: %v", err)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "folder opened in file explorer",
			"path":    dir,
		})
	})

	// 12. POST /api/pair/initiate — Initiates in-band pairing with a remote peer address
	mux.HandleFunc("/api/pair/initiate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		var req struct {
			PeerAddr string `json:"peer_addr"`
		}
		releaseBody := boundControlBody(w, r, 8*1024)
		defer releaseBody()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PeerAddr) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid request: peer_addr is required"})
			return
		}
		peerAddr := strings.TrimSpace(req.PeerAddr)
		h.logger.Action(DomainPeer, fmt.Sprintf("Initiating in-band pairing with %s", peerAddr))
		if !acquireSlot(h.pairSlots) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "message": "too many concurrent pairing attempts, retry later"})
			return
		}
		defer releaseSlot(h.pairSlots)
		pairCtx, pairCancel := h.withRunContext(r.Context())
		defer pairCancel()
		pairingOptions := pairing.PairingOptions{}
		if actual := h.P2PAddr(); actual != nil {
			_, portText, splitErr := net.SplitHostPort(actual.String())
			if splitErr == nil {
				if port, convErr := strconv.Atoi(portText); convErr == nil {
					pairingOptions.LocalListenPort = port
				}
			}
		}
		res, err := pairing.DialInBandPairingWithOptions(pairCtx, peerAddr, h.store, func(peerSAS, localSAS string) bool {
			if h.cfg.AutoAcceptPairing {
				h.logger.Action(DomainPeer, fmt.Sprintf("Auto-accepted pairing SAS match with %s: %s vs %s", peerAddr, peerSAS, localSAS))
				return true
			}

			pairID := newPendingPairingID()
			decisionCh := make(chan struct{})
			pending := &PendingPairing{
				ID:         pairID,
				RemoteAddr: peerAddr,
				PeerSAS:    peerSAS,
				LocalSAS:   localSAS,
				PeerName:   "Remote Device",
				CreatedAt:  time.Now(),
				decisionCh: decisionCh,
			}
			h.pendingPairingsMu.Lock()
			if len(h.pendingPairings) >= 10 {
				h.pendingPairingsMu.Unlock()
				h.logger.Warn(DomainPeer, "Pairing request rejected: too many pending requests")
				return false
			}
			h.pendingPairings[pairID] = pending
			h.pendingPairingsMu.Unlock()
			defer func() {
				h.pendingPairingsMu.Lock()
				delete(h.pendingPairings, pairID)
				h.pendingPairingsMu.Unlock()
			}()

			h.logger.Action(DomainPeer, fmt.Sprintf("🔐 Web pairing approval required: %s (SAS: %s, ID: %s)", peerAddr, peerSAS, pairID))
			select {
			case <-decisionCh:
				h.pendingPairingsMu.Lock()
				accepted := pending.decision
				h.pendingPairingsMu.Unlock()
				return accepted
			case <-pairCtx.Done():
				return h.terminatePendingPairing(pending, false)
			case <-time.After(60 * time.Second):
				h.logger.Warn(DomainPeer, fmt.Sprintf("Pairing request from %s timed out", peerAddr))
				return h.terminatePendingPairing(pending, false)
			}
		}, pairingOptions)
		if err != nil {
			h.logger.Error(DomainPeer, fmt.Sprintf("Pairing with %s failed: %v", peerAddr, err))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "error": err.Error()})
			return
		}
		if res == nil || !res.Accepted {
			peerName := ""
			peerSAS := ""
			peerFP := ""
			if res != nil {
				peerSAS = res.PeerSAS
				peerFP = res.PeerFingerprint
				if h.store != nil {
					if p, ok := h.store.GetPeer(peerFP); ok {
						peerName = p.DisplayName()
					}
				}
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":           "rejected",
				"accepted":         false,
				"peer_addr":        peerAddr,
				"peer_name":        peerName,
				"peer_sas":         peerSAS,
				"peer_fingerprint": peerFP,
			})
			return
		}
		h.logger.Action(DomainPeer, fmt.Sprintf("✅ Successfully paired with %s (SAS: %s)", peerAddr, res.PeerSAS))
		peerName := ""
		if h.store != nil {
			if p, ok := h.store.GetPeer(res.PeerFingerprint); ok {
				peerName = p.DisplayName()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "paired",
			"accepted":         true,
			"peer_addr":        peerAddr,
			"peer_name":        peerName,
			"peer_sas":         res.PeerSAS,
			"peer_fingerprint": res.PeerFingerprint,
		})
	})

	// 12a. GET /api/pair/pending — Returns list of pending incoming pairing requests
	mux.HandleFunc("/api/pair/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, h.ListPendingPairings())
	})

	// 12b. POST /api/pair/decision — Approves or rejects a pending pairing request
	mux.HandleFunc("/api/pair/decision", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, 8*1024)
		defer releaseBody()
		var req struct {
			ID     string `json:"id"`
			Accept bool   `json:"accept"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid request: id is required"})
			return
		}
		ok := h.ResolvePendingPairing(req.ID, req.Accept)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"status": "error", "message": "pending pairing not found or expired"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"id":       req.ID,
			"accepted": req.Accept,
		})
	})

	// 13. POST /api/peers/active — Set active peer
	mux.HandleFunc("/api/peers/active", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
		var req struct {
			Peer string `json:"peer"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Peer = r.FormValue("peer")
		}

		if err := h.SetActivePeer(req.Peer); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "success",
			"active_peer": h.GetActivePeer(),
		})
	})

	// 14. POST /api/peers/alias — Set peer alias
	mux.HandleFunc("/api/peers/alias", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
		var req struct {
			Fingerprint string `json:"fingerprint"`
			Alias       string `json:"alias"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
			req.Alias = r.FormValue("alias")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.SetAlias(req.Fingerprint, req.Alias); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "alias updated",
		})
	})

	// 15. POST /api/peers/default — Set default peer
	mux.HandleFunc("/api/peers/default", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
		var req struct {
			Fingerprint string `json:"fingerprint"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.SetDefault(req.Fingerprint); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "default peer updated",
		})
	})

	// 16. POST /api/peers/remove — Remove / unpair peer
	mux.HandleFunc("/api/peers/remove", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}
		releaseBody := boundControlBody(w, r, maxControlBodySize)
		defer releaseBody()
		var req struct {
			Fingerprint string `json:"fingerprint"`
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "invalid json body"})
				return
			}
		} else {
			req.Fingerprint = r.FormValue("fingerprint")
		}

		if req.Fingerprint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "fingerprint is required"})
			return
		}
		if h.store == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"status": "error", "message": "peer store not available"})
			return
		}
		if err := h.store.RemovePeer(req.Fingerprint); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": err.Error()})
			return
		}
		if h.GetActivePeer() == req.Fingerprint {
			_ = h.SetActivePeer("")
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "peer removed",
		})
	})

	// 17. GET /api/discovery/peers — Discovered nearby Hubs on LAN
	mux.HandleFunc("/api/discovery/peers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Tantu-IPC-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"status": "error", "message": "method not allowed"})
			return
		}

		discovered := make([]discoveredPeerResponse, 0)
		if engine := h.DiscoveryEngine(); engine != nil {
			nodes := engine.ListNodes()
			for _, node := range nodes {
				isPaired := false
				if h.store != nil {
					if _, ok := h.store.GetPeer(node.Fingerprint); ok {
						isPaired = true
					}
				}
				discovered = append(discovered, discoveredPeerResponse{
					Name:        node.InstanceName,
					Address:     node.Address,
					Port:        node.Port,
					SAS:         node.SAS,
					Fingerprint: node.Fingerprint,
					IsPaired:    isPaired,
				})
			}
		}
		writeJSON(w, http.StatusOK, discovered)
	})
}

// maxInlinePreviewBytes caps inline dashboard previews. Larger images are
// served as downloads only, so opening the inbox can never pull gigabytes
// into the browser process.
const maxInlinePreviewBytes = 8 * 1024 * 1024

// inlinePreviewTypes maps file extensions eligible for inline preview to
// their fixed content types. SVG/HTML and everything unlisted are deliberately
// absent: only inert raster formats may render in the dashboard origin.
var inlinePreviewTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
	".ico":  "image/x-icon",
}

// serveReceivedFile serves one received file addressed by drop ID. The
// recorded SavedPath must resolve inside outputDir and be a regular file;
// symlinks, directories, and escapes fail closed with 404 (no existence
// oracle beyond what the recent-items feed already discloses).
func serveReceivedFile(w http.ResponseWriter, r *http.Request, outputDir string, item ReceivedDropItem) {
	abs, err := filepath.Abs(item.SavedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	outAbs, err := filepath.Abs(outputDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Resolve symlinks on both sides before the containment check: a
	// purely lexical Rel check would pass a symlinked directory component
	// (or a symlinked OutputDir itself) that escapes at open time.
	absEval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	outEval, err := filepath.EvalSymlinks(outAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rel, err := filepath.Rel(outEval, absEval)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	inspected, err := os.Lstat(absEval)
	if err != nil || !inspected.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(absEval)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// The descriptor is bound at open; verify it is still the inspected
	// regular file so a leaf swap between Lstat and Open fails closed
	// instead of serving a victim file. (Directory-component swaps inside
	// the residual Lstat→Open window remain a same-user local race.)
	finfo, err := f.Stat()
	if err != nil || !finfo.Mode().IsRegular() || !os.SameFile(inspected, finfo) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("mode") == "inline" {
		if contentType, ok := inlinePreviewTypes[strings.ToLower(filepath.Ext(item.Name))]; ok && finfo.Size() > 0 && finfo.Size() <= maxInlinePreviewBytes {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", "inline")
			// Defense in depth for direct navigation: a mislabeled file
			// opened as a top-level document gets no script execution.
			// (For <img> embeds the protection comes from the image
			// decoder plus nosniff; sandbox does not constrain
			// subresources.)
			w.Header().Set("Content-Security-Policy", "sandbox")
			http.ServeContent(w, r, item.Name, finfo.ModTime(), f)
			return
		}
	}
	safeName := SanitizeDropFilename(item.Name)
	if safeName == "" {
		safeName = "download"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(safeName, `"`, "_")+`"`)
	http.ServeContent(w, r, item.Name, finfo.ModTime(), f)
}

func openDirectoryInOS(dir string) error {
	cleanDir := filepath.Clean(dir)
	// Resolve to an absolute path: a relative OutputDir such as "-n" would
	// otherwise be parsed as a helper flag by open/xdg-open. Absolute paths
	// can never begin with '-'. A resolution failure fails closed rather
	// than executing the helper with the raw relative value.
	absDir, err := filepath.Abs(cleanDir)
	if err != nil {
		return fmt.Errorf("resolve directory path: %w", err)
	}
	cleanDir = absDir
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", cleanDir)
	case "darwin":
		cmd = exec.Command("open", cleanDir)
	default:
		cmd = exec.Command("xdg-open", cleanDir)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
