"use client";

import Link from "next/link";
import { useState, type ComponentType, type MouseEvent, type SVGProps } from "react";
import { IconBook, IconHeadset, IconMoon, IconSun, IconSwitch } from "./icons";

export type RailItem = { id: string; label: string; icon: ComponentType<SVGProps<SVGSVGElement>> };
type RailLink = { href: string; label: string };

export const DOCS = "https://github.com/RosieOh/IPCC_PlayGround/blob/main/docs";

const STEP = 48; // 버튼 40px + 간격 8px

/**
 * 화면 가장자리에서 떨어져 떠 있는 아이콘 레일. 한 화면짜리라 메뉴는 섹션으로 이동한다.
 * 현재 위치는 유리 물방울(.rail-blob)이 표시하고, 마우스를 올린 메뉴로 미끄러져 간다.
 * 아래쪽에는 다른 현황판으로 가는 링크(other)와 설계 문서(doc)가 있다.
 */
export function Rail({ items, active, doc, other }: { items: RailItem[]; active: string; doc: RailLink; other: RailLink }) {
  const [hover, setHover] = useState<number | null>(null);
  const activeIndex = Math.max(
    0,
    items.findIndex((it) => it.id === active),
  );
  const blobIndex = hover ?? activeIndex;

  return (
    <aside className="rail glass" aria-label="화면 메뉴">
      <div className="flex flex-col items-center gap-5">
        <a href={`#${items[0]?.id ?? ""}`} className="brand-mark" aria-label="맨 위로">
          <IconHeadset className="size-[18px]" />
        </a>
        <ul className="rail-list" onMouseLeave={() => setHover(null)}>
          <span className="rail-blob" style={{ transform: `translateY(${blobIndex * STEP}px)` }} aria-hidden />
          {items.map((it, i) => (
            <li key={it.id}>
              <a
                href={`#${it.id}`}
                className="rail-link"
                aria-label={it.label}
                aria-current={it.id === active ? "true" : undefined}
                onMouseEnter={() => setHover(i)}
                onFocus={() => setHover(i)}
                onBlur={() => setHover(null)}
              >
                <it.icon />
                <span className="tip">{it.label}</span>
              </a>
            </li>
          ))}
        </ul>
      </div>
      <div className="flex flex-col items-center gap-2">
        <div className="rail-sep" />
        <Link className="rail-link" href={other.href} aria-label={other.label}>
          <IconSwitch />
          <span className="tip">{other.label}</span>
        </Link>
        <a className="rail-link" href={doc.href} target="_blank" rel="noreferrer" aria-label={doc.label}>
          <IconBook />
          <span className="tip">{doc.label}</span>
        </a>
        <button type="button" className="rail-link" aria-label="테마 바꾸기" onClick={toggleTheme}>
          <IconSun className="hidden dark:block" />
          <IconMoon className="block dark:hidden" />
          <span className="tip">밝게 / 어둡게</span>
        </button>
      </div>
    </aside>
  );
}

/** 테마를 바꾸고 저장한다. 지원 브라우저에서는 누른 자리에서 새 테마가 원으로 번진다. */
function toggleTheme(e: MouseEvent<HTMLButtonElement>) {
  const root = document.documentElement;
  const apply = () => {
    const dark = root.classList.toggle("dark");
    try {
      localStorage.setItem("ipcc-theme", dark ? "dark" : "light");
    } catch {
      /* 저장할 수 없어도 이번 화면에서는 바뀐다 */
    }
  };
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (!document.startViewTransition || reduced) {
    apply();
    return;
  }
  const r = e.currentTarget.getBoundingClientRect();
  const x = r.left + r.width / 2;
  const y = r.top + r.height / 2;
  root.style.setProperty("--vt-x", `${x}px`);
  root.style.setProperty("--vt-y", `${y}px`);
  root.style.setProperty("--vt-r", `${Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y))}px`);
  document.startViewTransition(apply);
}
