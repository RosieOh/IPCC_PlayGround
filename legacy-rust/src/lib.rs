//! IPCC 상태머신 — Rust '레거시' 구현.
//!
//! 입력: AMI 녹화(받은 바이트 그대로) → 시각이 붙은 이벤트·스냅샷·명령
//! 출력: 상태 변화 JSONL (docs/06-state-model.md 10절). Go 포팅(engine/state)과 바이트 단위로 같아야 하고,
//! `make diff-test`가 모든 녹화에서 이를 확인한다.

pub mod ami;
pub mod engine;
pub mod json;
pub mod replay;
