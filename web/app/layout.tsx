import type { Metadata, Viewport } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "IPCC 실시간 현황판",
  description: "상태머신 엔진(bin/engine)의 상담원·콜 상태를 실시간으로 보는 학습용 현황판",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#edf1f8" },
    { media: "(prefers-color-scheme: dark)", color: "#05070d" },
  ],
};

// 첫 그림 전에 테마를 정한다 (저장값이 없으면 시스템 설정). 그래야 밝은 화면이 번쩍이지 않는다
const themeScript = `try{var t=localStorage.getItem("ipcc-theme");var d=t==="dark"||(t===null&&matchMedia("(prefers-color-scheme: dark)").matches);document.documentElement.classList.toggle("dark",d)}catch(e){}`;

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ko" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
        {/* Pretendard (SIL OFL). 못 받으면 시스템 한글 글꼴로 보인다 */}
        <link
          rel="stylesheet"
          href="https://cdn.jsdelivr.net/gh/orioncactus/pretendard@v1.3.9/dist/web/variable/pretendardvariable-dynamic-subset.min.css"
        />
      </head>
      <body>{children}</body>
    </html>
  );
}
