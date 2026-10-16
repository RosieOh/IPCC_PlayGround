#!/bin/sh
# 컨테이너 시작 스크립트
#   1) EXTERNAL_IP 환경변수로 SIP 트랜스포트(NAT) 설정 파일을 만든다
#   2) 시간대와 로그 디렉터리를 준비한다
#   3) Asterisk를 포그라운드(-f)로 실행한다 → `docker compose logs`로 출력이 보인다
set -eu

# Asterisk는 시간대를 TZ 환경변수가 아니라 /etc/localtime에서 읽는다
if [ -n "${TZ:-}" ] && [ -f "/usr/share/zoneinfo/${TZ}" ]; then
    ln -snf "/usr/share/zoneinfo/${TZ}" /etc/localtime
    echo "${TZ}" > /etc/timezone
fi

EXTERNAL_IP="${EXTERNAL_IP:-127.0.0.1}"
TEMPLATE=/etc/asterisk/pjsip_transport.conf.tmpl
TARGET=/etc/asterisk-generated/pjsip_transport.conf

sed "s/@EXTERNAL_IP@/${EXTERNAL_IP}/g" "$TEMPLATE" > "$TARGET"
echo "[entrypoint] EXTERNAL_IP=${EXTERNAL_IP} -> ${TARGET}"

# 빈 디렉터리를 마운트하면 make install이 만들어 둔 하위 디렉터리가 가려진다
mkdir -p /var/log/asterisk/cdr-csv /var/log/asterisk/cdr-custom /var/spool/asterisk/monitor
chown -R asterisk:asterisk /var/log/asterisk /var/spool/asterisk/monitor 2>/dev/null || true

# -f: 포크하지 않음(포그라운드)  -n: 색상 끔  -T: 줄마다 시각 표시  -vvv: verbose 3
exec asterisk -f -n -T -vvv "$@"
