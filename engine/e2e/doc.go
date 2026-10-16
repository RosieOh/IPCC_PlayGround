// Package e2e는 실제 Asterisk(docker compose)에 붙어서 콜 흐름 전체를 검증하는 테스트다.
//
// 테스트 파일에 e2e 빌드 태그가 붙어 있어서 평소 go test ./...에서는 돌지 않는다.
// 저장소 루트에서 실행한다.
//
//	make e2e                  Asterisk를 띄우고 이 테스트를 실행
//	E2E_RESTART=1 make e2e    Asterisk 재시작 → 자동 재접속까지 검증 (CI에서 사용)
//
// 환경변수: AMI_ADDR, AMI_USER, AMI_SECRET (기본값은 manager.conf와 같다),
// E2E_RECORD_DIR (AMI 녹화를 남길 디렉터리, 기본은 임시 디렉터리)
package e2e
