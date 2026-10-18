// 유리 패널 뒤의 공간: 천천히 떠도는 빛 덩어리와 흘러가는 원근 격자 바닥 (CSS만, globals.css .world)
export function World() {
  return (
    <div className="world" aria-hidden>
      <div className="orb orb-1" />
      <div className="orb orb-2" />
      <div className="orb orb-3" />
      <div className="floor" />
    </div>
  );
}
