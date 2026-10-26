"use client";

import { useEffect, useRef, useState } from "react";
import { IconChevronDown } from "./icons";

export type PickOption = { value: string; label: string; hint?: string };

/** 버튼을 누르면 펼쳐지는 작은 선택 메뉴 (이석 사유, 전환할 상담원, 스킬 …). 바깥을 누르거나 Esc로 닫힌다 */
export function PickMenu({
  label,
  options,
  disabled,
  onPick,
  empty = "고를 것이 없어요",
}: {
  label: string;
  options: PickOption[];
  disabled: boolean;
  onPick: (value: string) => void;
  empty?: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === "Escape" : !ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <button
        className="btn btn-xs btn-secondary"
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {label}
        <IconChevronDown className="size-3.5" />
      </button>
      {open && (
        <div className="menu left-0 top-[calc(100%+6px)]" role="menu">
          {options.length === 0 ? (
            <p className="caption px-3 py-2">{empty}</p>
          ) : (
            options.map((o) => (
              <button
                key={o.value}
                role="menuitem"
                className="menu-item"
                onClick={() => {
                  setOpen(false);
                  onPick(o.value);
                }}
              >
                {o.label}
                {o.hint && <span className="mono ml-2">{o.hint}</span>}
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
}
