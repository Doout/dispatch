import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BracketsCurly, Check, Copy, DownloadSimple, MagnifyingGlass, TextAlignLeft } from "@phosphor-icons/react";
import { basicSetup, EditorView } from "codemirror";
import { foldEffect, HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Decoration, WidgetType } from "@codemirror/view";
import { json } from "@codemirror/lang-json";
import { yaml } from "@codemirror/lang-yaml";
import { tags } from "@lezer/highlight";
import { DeploymentManifest } from "../api";
import {
  DocumentRange,
  DocumentGap,
  createYamlPathLocator,
  filterDocument,
  formatNormalizedPath,
  MatchContext,
  parseManifest,
  renderJSON,
  searchManifest,
} from "./manifestSearch";

type DocumentFormat = "json" | "yaml";

const dispatchCodeHighlight = HighlightStyle.define([
  { tag: [tags.propertyName, tags.attributeName, tags.labelName], color: "#8fc5eb" },
  { tag: [tags.string, tags.special(tags.string)], color: "#a8cf91" },
  { tag: [tags.number, tags.bool, tags.null], color: "#d7b875" },
  { tag: [tags.keyword, tags.atom, tags.typeName], color: "#c1a7df" },
  { tag: [tags.variableName, tags.name], color: "#d5dee4" },
  { tag: [tags.comment, tags.meta], color: "#708491", fontStyle: "italic" },
  { tag: tags.invalid, color: "#f08a92", textDecoration: "underline" },
]);

export function StructuredDocumentViewer({ manifest }: { manifest: DeploymentManifest }) {
  const [format, setFormat] = useState<DocumentFormat>("yaml");
  const [query, setQuery] = useState("");
  const [selectedPath, setSelectedPath] = useState("");
  const [copied, setCopied] = useState(false);
  const [wrap, setWrap] = useState(false);
  const [context, setContext] = useState<MatchContext>({});

  const parsed = useMemo(() => {
    try {
      return { value: parseManifest(manifest.document) };
    } catch (cause) {
      return { error: cause instanceof Error ? cause.message : "The manifest could not be parsed.", value: undefined };
    }
  }, [manifest.document]);
  const jsonDocument = useMemo(() => parsed.error ? undefined : renderJSON(parsed.value), [parsed.error, parsed.value]);
  const locateYaml = useMemo(() => parsed.error ? () => ({}) : createYamlPathLocator(manifest.document), [manifest.document, parsed.error]);
  const search = useMemo(() => parsed.error ? { error: parsed.error, matches: [], mode: "key" as const } : searchManifest(parsed.value, query), [parsed.error, parsed.value, query]);
  const selected = search.matches.find((match) => match.path === selectedPath);
  const queryActive = Boolean(query.trim());
  const visibleDocument = useMemo(() => {
    const base = format === "json" && jsonDocument
      ? jsonDocument
      : { gaps: [], ranges: new Map(), text: manifest.document, valueRanges: new Map() };
    if (!queryActive) return base;
    if (search.error || !search.matches.length) return { gaps: [], ranges: new Map(), text: "", valueRanges: new Map() };

    return filterDocument(base.text, search.matches, context, (segments) => {
      if (format === "yaml") return locateYaml(segments);
      const path = formatNormalizedPath(segments);
      return { key: jsonDocument?.ranges.get(path), value: jsonDocument?.valueRanges.get(path) };
    });
  }, [context, format, jsonDocument, locateYaml, manifest.document, queryActive, search.error, search.matches]);
  const document = visibleDocument.text;
  const selection = selected ? visibleDocument.ranges.get(selected.path) : undefined;
  const folds = useMemo(() => queryActive ? search.matches.flatMap((match) => {
    const range = Array.isArray(match.value) && match.value.length > 1
      ? visibleDocument.valueRanges.get(match.path)
      : undefined;
    return range ? [range] : [];
  }) : [], [queryActive, search.matches, visibleDocument.valueRanges]);

  useEffect(() => {
    setQuery("");
    setSelectedPath("");
    setFormat("yaml");
    setContext({});
  }, [manifest.document]);

  useEffect(() => {
    if (!queryActive || !search.matches.length) {
      if (selectedPath) setSelectedPath("");
      return;
    }
    if (!search.matches.some((match) => match.path === selectedPath)) setSelectedPath(search.matches[0].path);
  }, [queryActive, search.matches, selectedPath]);

  useEffect(() => {
    if (format === "json" && !jsonDocument) setFormat("yaml");
  }, [format, jsonDocument]);

  const copy = async () => {
    await navigator.clipboard.writeText(document);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  };

  const download = () => {
    const blob = new Blob([document], { type: format === "json" ? "application/json" : "application/yaml" });
    const url = URL.createObjectURL(blob);
    const link = window.document.createElement("a");
    link.href = url;
    link.download = `${manifest.kind.toLocaleLowerCase()}-${manifest.name}.${format}`;
    link.click();
    URL.revokeObjectURL(url);
  };

  const expandGap = useCallback((gap: DocumentGap, direction: "all" | "down" | "up") => {
    const step = direction === "all" ? Number.MAX_SAFE_INTEGER : 3;
    setContext((current) => {
      const next = { ...current };
      if (direction === "all" || direction === "up") {
        gap.expandUp.forEach((path) => {
          const value = next[path] ?? { after: 0, before: 0 };
          next[path] = { ...value, before: direction === "all" ? step : value.before + step };
        });
      }
      if (direction === "all" || direction === "down") {
        gap.expandDown.forEach((path) => {
          const value = next[path] ?? { after: 0, before: 0 };
          next[path] = { ...value, after: direction === "all" ? step : value.after + step };
        });
      }
      return next;
    });
  }, []);

  return <section className={`structured-document-viewer${queryActive ? " has-results" : ""}`} aria-label="Resource manifest">
    <div className="structured-document-toolbar">
      <label className="structured-document-search">
        <MagnifyingGlass size={15} />
        <span className="sr-only">Filter manifest by key or JSONPath</span>
        <input
          type="search"
          aria-label="Filter manifest by key or JSONPath"
          value={query}
          disabled={Boolean(parsed.error)}
          placeholder="Key or JSONPath, for example image or $.spec"
          spellCheck={false}
          onChange={(event) => {
            setQuery(event.target.value);
            setContext({});
          }}
        />
        {query && !search.error && <small>{search.matches.length}</small>}
      </label>
      <div className="structured-document-tools">
        <div className="structured-document-format" role="group" aria-label="Manifest format">
          <button type="button" className={format === "yaml" ? "active" : ""} aria-pressed={format === "yaml"} onClick={() => setFormat("yaml")}>YAML</button>
          <button type="button" className={format === "json" ? "active" : ""} aria-pressed={format === "json"} disabled={!jsonDocument} onClick={() => setFormat("json")}>JSON</button>
        </div>
        <button type="button" className={wrap ? "active" : ""} aria-label="Toggle line wrapping" aria-pressed={wrap} title="Wrap lines" onClick={() => setWrap((value) => !value)}><TextAlignLeft size={16} /></button>
        <button type="button" aria-label={`Copy resource ${format.toUpperCase()}`} title={copied ? "Copied" : `Copy ${format.toUpperCase()}`} disabled={!document} onClick={() => void copy()}>{copied ? <Check size={16} /> : <Copy size={16} />}</button>
        <button type="button" aria-label={`Download resource ${format.toUpperCase()}`} title={`Download ${format.toUpperCase()}`} disabled={!document} onClick={download}><DownloadSimple size={16} /></button>
      </div>
    </div>
    <div className="structured-document-workspace">
      {queryActive && <aside className="structured-document-results" aria-label="Manifest matches">
        <header><div><BracketsCurly size={15} /><strong>{search.mode === "jsonpath" ? "JSONPath" : "Key matches"}</strong></div>{!search.error && <span>{search.matches.length}</span>}</header>
        {search.error && <p className="structured-document-error" role="alert">{search.error}</p>}
        {!search.error && search.matches.length === 0 && <p className="structured-document-empty">No matching keys.</p>}
        {!search.error && search.matches.length > 0 && <div role="list">{search.matches.map((match) => <button
          key={match.path}
          type="button"
          role="listitem"
          aria-label={`${match.key}: ${match.preview}`}
          className={match.path === selectedPath ? "active" : ""}
          title={match.path}
          onClick={() => setSelectedPath(match.path)}
        >
          <strong>{match.key}</strong>
          <code>{match.path}</code>
          <span>{match.preview}</span>
        </button>)}</div>}
      </aside>}
      {queryActive && !document
        ? <div className="structured-document-editor-empty">{search.error ? "Fix the JSONPath to view matching lines." : "No matching lines."}</div>
        : <ManifestEditor document={document} format={format} folds={folds} gaps={visibleDocument.gaps} onExpandGap={expandGap} selection={selection} wrap={wrap} />}
    </div>
  </section>;
}

function ManifestEditor({ document, format, folds, gaps, onExpandGap, selection, wrap }: { document: string; format: DocumentFormat; folds: DocumentRange[]; gaps: DocumentGap[]; onExpandGap: (gap: DocumentGap, direction: "all" | "down" | "up") => void; selection?: DocumentRange; wrap: boolean }) {
  const parentRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);

  useEffect(() => {
    if (!parentRef.current) return;
    const parent = parentRef.current;
    parent.replaceChildren();
    const view = new EditorView({
      doc: document,
      extensions: [
        basicSetup,
        EditorView.editable.of(false),
        EditorView.contentAttributes.of({ "aria-label": `${format.toUpperCase()} manifest` }),
        format === "json" ? json() : yaml(),
        syntaxHighlighting(dispatchCodeHighlight),
        contextGapDecorations(gaps, document.length, onExpandGap),
        ...(wrap ? [EditorView.lineWrapping] : []),
        EditorView.theme({
          "&": { height: "100%", color: "#d5dee4", backgroundColor: "#10181f" },
          ".cm-scroller": { backgroundColor: "#10181f", fontFamily: "var(--mono)", fontSize: "10px", lineHeight: "1.65", overflow: "auto" },
          ".cm-content": { padding: "13px 0 28px" },
          ".cm-line": { caretColor: "#8fc5eb" },
          ".cm-gutters": { backgroundColor: "#10181f", borderRight: "1px solid #25343e", color: "#617581" },
          ".cm-gutterElement": { paddingInline: "7px" },
          ".cm-activeLine": { backgroundColor: "rgba(98, 145, 178, .08)" },
          ".cm-activeLineGutter": { backgroundColor: "rgba(98, 145, 178, .08)", color: "#a9bbc6" },
          ".cm-foldPlaceholder": { backgroundColor: "#21313b", borderColor: "#3a505e", color: "#aebfc9" },
          ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "#244b68 !important" },
          ".cm-matchingBracket": { color: "#e3edf2", backgroundColor: "#28465a", outline: "1px solid #3b617a" },
        }, { dark: true }),
      ],
      parent,
    });
    if (folds.length) {
      view.dispatch({
        effects: folds
          .filter((range) => range.to > range.from)
          .map((range) => foldEffect.of(range)),
      });
    }
    viewRef.current = view;
    return () => {
      view.destroy();
      view.dom.remove();
      viewRef.current = null;
    };
  }, [document, folds, format, gaps, onExpandGap, wrap]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view || !selection) return;
    const from = Math.min(selection.from, view.state.doc.length);
    const to = Math.min(Math.max(selection.to, from), view.state.doc.length);
    view.dispatch({ selection: { anchor: from, head: to }, scrollIntoView: true });
  }, [selection]);

  return <div ref={parentRef} className="structured-document-editor" />;
}

function contextGapDecorations(gaps: DocumentGap[], documentLength: number, onExpand: (gap: DocumentGap, direction: "all" | "down" | "up") => void) {
  return EditorView.decorations.of(Decoration.set(gaps.map((gap) => Decoration.widget({
    block: true,
    side: gap.at === documentLength ? 1 : -1,
    widget: new ContextGapWidget(gap, onExpand),
  }).range(gap.at)), true));
}

class ContextGapWidget extends WidgetType {
  constructor(private readonly gap: DocumentGap, private readonly onExpand: (gap: DocumentGap, direction: "all" | "down" | "up") => void) {
    super();
  }

  toDOM() {
    const row = window.document.createElement("div");
    row.className = "cm-structured-document-gap";
    if (this.gap.expandUp.length) row.append(this.button("up", "↑", "Show lines above"));
    row.append(this.button("all", "↕", "Show all hidden lines"));
    if (this.gap.expandDown.length) row.append(this.button("down", "↓", "Show lines below"));
    return row;
  }

  ignoreEvent() {
    return true;
  }

  private button(direction: "all" | "down" | "up", symbol: string, label: string) {
    const button = window.document.createElement("button");
    button.type = "button";
    button.textContent = symbol;
    button.title = label;
    button.setAttribute("aria-label", label);
    button.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      this.onExpand(this.gap, direction);
    });
    return button;
  }
}
