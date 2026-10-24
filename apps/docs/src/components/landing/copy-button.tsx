"use client";

import { useRef, useState } from "react";

/** Copies `text`, then says so for a moment. Falls back to selecting `target`. */
export function CopyButton({
  text,
  label,
  targetId,
}: {
  text: string;
  label: string;
  targetId: string;
}) {
  const [note, setNote] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const flash = (value: string) => {
    setNote(value);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setNote(""), 1600);
  };

  const select = () => {
    const el = document.getElementById(targetId);
    if (!el) return;
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel?.removeAllRanges();
    sel?.addRange(range);
    flash("Selected");
  };

  return (
    <button
      className="copy"
      type="button"
      aria-label={label}
      onClick={() => {
        try {
          navigator.clipboard
            .writeText(text)
            .then(() => flash("Copied"), select);
        } catch {
          select();
        }
      }}
    >
      <svg
        width="15"
        height="15"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        aria-hidden="true"
      >
        <rect x="9" y="9" width="12" height="12" rx="2" />
        <path d="M5 15V5a2 2 0 0 1 2-2h10" />
      </svg>
      <span className="copy-label" aria-live="polite">
        {note}
      </span>
    </button>
  );
}
