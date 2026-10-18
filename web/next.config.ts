import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  poweredByHeader: false,
  // 개발 도구 버튼이 왼쪽 아래 레일(테마 버튼)을 가리지 않게
  devIndicators: { position: "bottom-right" },
};

export default nextConfig;
