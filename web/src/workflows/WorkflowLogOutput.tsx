import { useEffect, useRef } from "react";

export function WorkflowLogOutput({ text, follow, emptyText = "Waiting for command output…" }: { text: string; follow: boolean; emptyText?: string }) {
  const element = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (follow && element.current) element.current.scrollTop = element.current.scrollHeight;
  }, [text, follow]);
  return <pre ref={element} tabIndex={0}>{text || emptyText}</pre>;
}
