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

# 컨테이너 자신의 IPv4. local_net에 들어가야 Asterisk가 SDP의 자기 주소를 EXTERNAL_IP로 바꿔 적는다.
# 컨테이너를 다시 만들면 바뀔 수 있어서 시작할 때마다 다시 찾는다.
CONTAINER_IP="$(hostname -i | tr ' ' '\n' | grep -E '^[0-9]+(\.[0-9]+){3}$' | head -n1 || true)"
if [ -z "$CONTAINER_IP" ]; then
    echo "[entrypoint] WARNING: 컨테이너 IP를 찾지 못함 — Mac에서 건 콜의 음성이 안 들릴 수 있다" >&2
    CONTAINER_IP=127.0.0.1
fi

sed -e "s/@EXTERNAL_IP@/${EXTERNAL_IP}/g" -e "s/@CONTAINER_IP@/${CONTAINER_IP}/g" "$TEMPLATE" > "$TARGET"
echo "[entrypoint] EXTERNAL_IP=${EXTERNAL_IP} CONTAINER_IP=${CONTAINER_IP} -> ${TARGET}"

# 빈 디렉터리를 마운트하면 make install이 만들어 둔 하위 디렉터리가 가려진다
mkdir -p /var/log/asterisk/cdr-csv /var/log/asterisk/cdr-custom /var/spool/asterisk/monitor
chown -R asterisk:asterisk /var/log/asterisk /var/spool/asterisk/monitor 2>/dev/null || true

# -f: 포크하지 않음(포그라운드)  -n: 색상 끔  -T: 줄마다 시각 표시  -vvv: verbose 3
exec asterisk -f -n -T -vvv "$@"
